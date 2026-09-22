package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mopay/collections-api/internal/ledgerclient"
	"github.com/mopay/collections-api/internal/platform"
	"github.com/mopay/collections-api/internal/store"
)

type createCollectionBody struct {
	Channel           platform.Channel `json:"channel"`
	AmountMinor       platform.Minor   `json:"amountMinor"`
	CustomerReference string           `json:"customerReference"`
	MerchantReference string           `json:"merchantReference"`
	// Currency is deliberately NOT read. C-1.8 derives it from the channel; accepting it
	// from a caller would let a merchant declare KES on a Nigerian channel.
}

func (a *API) createCollection(w http.ResponseWriter, r *http.Request) error {
	claims := claimsFrom(r.Context())
	ctx := r.Context()
	log := platform.LoggerFrom(ctx, a.log)

	var body createCollectionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"request body is not valid JSON: %v", err)
	}
	if body.MerchantReference == "" {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"merchantReference is required: it is the idempotency key (C-1.3)")
	}
	if body.AmountMinor <= 0 {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"amountMinor must be a positive integer in minor units, got %d", body.AmountMinor)
	}
	if !platform.ValidChannel(body.Channel) {
		return platform.Errorf(http.StatusBadRequest, platform.CodeUnsupportedChannel,
			"channel %q is not supported (supported: %v)", body.Channel, platform.SupportedChannels())
	}

	// C-6.2: the merchant is taken from the token, never from the request body.
	merchantID := claims.MerchantID

	// C-1.3: a repeat returns the original and creates nothing.
	if existing, err := a.st.CollectionByMerchantReference(ctx, merchantID, body.MerchantReference); err != nil {
		return err
	} else if existing != nil {
		log.Info("idempotent replay of collection intake; returning the original",
			slog.String("collection_id", existing.ID),
			slog.String("merchant_reference", existing.MerchantReference))
		a.reg.IncCounter("collections_idempotent_replays_total",
			"Intake requests that matched an existing merchantReference.", nil)
		return writeJSON(w, http.StatusOK, existing)
	}

	merchant, err := a.resolveMerchant(ctx, merchantID)
	if err != nil {
		return err
	}

	// ---- C-1.5 float limit, FAIL CLOSED -------------------------------------
	balance, err := a.ledger.Balance(ctx, merchantID)
	if err != nil {
		// The PRD is explicit and the build plan repeats it: if the balance cannot be
		// determined, REFUSE the collection. Do not fail open. Accepting money we cannot
		// account for is the worse failure.
		log.Error("refusing collection: unsettled balance could not be determined from ledger",
			slog.String("merchant_reference", body.MerchantReference),
			slog.String("channel", string(body.Channel)),
			slog.String("error", err.Error()))
		a.reg.IncCounter("collections_rejected_total",
			"Collections refused at intake.", platform.Labels{"reason": "ledger_unavailable"})
		return platform.Errorf(http.StatusServiceUnavailable, platform.CodeUpstreamUnavailable,
			"cannot determine merchant %s unsettled balance, so the collection is refused: %v",
			merchantID, err)
	}

	// The check is on the projected balance: this collection would take the merchant past
	// the limit. A merchant already over the limit is refused by the same comparison.
	projected := balance.AmountMinor + body.AmountMinor
	if projected > merchant.FloatLimitMinor {
		log.Warn("refusing collection: float limit would be exceeded",
			slog.String("merchant_reference", body.MerchantReference),
			slog.Int64("unsettled_balance_minor", int64(balance.AmountMinor)),
			slog.Int64("float_limit_minor", int64(merchant.FloatLimitMinor)))
		a.reg.IncCounter("collections_rejected_total",
			"Collections refused at intake.", platform.Labels{"reason": "float_limit_exceeded"})

		// The error carries the balance and the limit so the merchant's integration can
		// act on it programmatically (C-1.5).
		return platform.Errorf(http.StatusConflict, platform.CodeFloatLimitExceeded,
			"merchant %s unsettled balance %s %s plus this collection %s would exceed the float limit %s %s",
			merchantID, balance.AmountMinor, merchant.FloatLimitCurrency,
			body.AmountMinor, merchant.FloatLimitMinor, merchant.FloatLimitCurrency).
			WithDetail("unsettledBalanceMinor", int64(balance.AmountMinor)).
			WithDetail("floatLimitMinor", int64(merchant.FloatLimitMinor)).
			WithDetail("requestedAmountMinor", int64(body.AmountMinor)).
			WithDetail("currency", string(merchant.FloatLimitCurrency))
	}

	col, existed, err := a.st.CreateCollection(ctx, store.CreateCollectionRequest{
		MerchantID:        merchantID,
		Channel:           body.Channel,
		AmountMinor:       body.AmountMinor,
		CustomerReference: body.CustomerReference,
		MerchantReference: body.MerchantReference,
	})
	if err != nil {
		return err
	}
	if existed {
		return writeJSON(w, http.StatusOK, col)
	}

	log.Info("collection accepted",
		slog.String("collection_id", col.ID),
		slog.String("channel", string(col.Channel)),
		slog.String("status", col.Status))
	a.reg.IncCounter("collections_accepted_total", "Collections accepted at intake.",
		platform.Labels{"channel": string(col.Channel)})

	return writeJSON(w, http.StatusCreated, col)
}

type callbackBody struct {
	CollectionID string `json:"collectionId"`
	Outcome      string `json:"outcome"` // cleared | failed
}

// channelCallback moves a collection out of pending (C-1.7) and, on clearing, writes the
// balancing ledger entries for it (C-3.1).
func (a *API) channelCallback(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	log := platform.LoggerFrom(ctx, a.log)

	var body callbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"callback body is not valid JSON: %v", err)
	}
	if body.CollectionID == "" {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"callback is missing collectionId")
	}

	col, changed, err := a.st.ApplyCallback(ctx, body.CollectionID, body.Outcome, time.Now().UTC())
	if err != nil {
		return err
	}
	// The ledger write is attempted whenever the collection is cleared - NOT only when
	// this particular call cleared it.
	//
	// The status update and the ledger write are two separate systems and cannot be one
	// transaction. If the ledger is unreachable in between, the collection is left
	// cleared with no ledger entries: money we have taken but not recorded. The
	// settlement worker keys on 'cleared', so it would then pay out against a credit
	// that was never written and drive the merchant's balance negative.
	//
	// Channels retry callbacks, so the repair path is the retry. WriteGroup is
	// idempotent on transactionGroupId, which makes re-attempting it safe and makes a
	// replay self-healing rather than a no-op.
	if col.Status == store.StatusCleared {
		// Money becomes ours to account for only once the channel clears it.
		// Sign convention lives in migration 003.
		group := ledgerclient.EntryGroup{
			TransactionGroupID: "tg_col_" + col.ID,
			EventType:          "collection",
			EventID:            col.ID,
			Entries: []ledgerclient.Entry{
				{MerchantID: col.MerchantID, Direction: "credit", AmountMinor: col.AmountMinor, Currency: col.Currency},
				{MerchantID: a.cfg.HouseAccountID, Direction: "debit", AmountMinor: col.AmountMinor, Currency: col.Currency},
			},
		}
		if err := a.ledger.WriteGroup(ctx, group); err != nil {
			log.Error("collection cleared but its ledger entries could not be written",
				slog.String("collection_id", col.ID),
				slog.String("transaction_group_id", group.TransactionGroupID),
				slog.String("error", err.Error()))
			return platform.Errorf(http.StatusBadGateway, platform.CodeUpstreamUnavailable,
				"collection %s cleared but the ledger write for group %s failed: %v",
				col.ID, group.TransactionGroupID, err)
		}
	}

	if !changed {
		// Replay. Idempotent by design - nothing changes and this is not an error.
		// The ledger write above has already run, so a replay also repairs a collection
		// that cleared while the ledger was unreachable.
		log.Info("channel callback replayed; no change made",
			slog.String("collection_id", col.ID), slog.String("status", col.Status))
		return writeJSON(w, http.StatusOK, map[string]any{"collection": col, "changed": false})
	}

	log.Info("channel callback applied",
		slog.String("collection_id", col.ID), slog.String("status", col.Status))
	a.reg.IncCounter("channel_callbacks_total", "Channel callbacks applied.",
		platform.Labels{"outcome": col.Status})

	return writeJSON(w, http.StatusOK, map[string]any{"collection": col, "changed": true})
}

func (a *API) getCollection(w http.ResponseWriter, r *http.Request) error {
	claims := claimsFrom(r.Context())
	col, err := a.st.Collection(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}

	// C-6.2: server-side scoping. A merchant asking for another merchant's collection
	// gets 404, not a filtered view - the id's existence is itself not theirs to learn.
	if col.MerchantID != claims.MerchantID {
		platform.LoggerFrom(r.Context(), a.log).Warn("authorization denied",
			slog.String("subject", claims.Subject),
			slog.String("action", "GET"),
			slog.String("resource", "collection/"+col.ID))
		return platform.Errorf(http.StatusNotFound, platform.CodeNotFound,
			"collection %s does not exist", r.PathValue("id"))
	}
	// C-1.9: the collection with its ledger entries. A ledger failure degrades the
	// detail view rather than failing it - the collection itself is still readable.
	entries, err := a.ledger.EntriesForCollection(r.Context(), col.ID)
	if err != nil {
		platform.LoggerFrom(r.Context(), a.log).Warn(
			"collection detail served without ledger entries: the ledger read failed",
			slog.String("collection_id", col.ID),
			slog.String("error", err.Error()))
		entries = nil
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"collection":    col.Redact(claims.Role),
		"ledgerEntries": entries,
	})
}

func (a *API) searchCollections(w http.ResponseWriter, r *http.Request) error {
	claims := claimsFrom(r.Context())
	q := r.URL.Query()

	p := store.SearchParams{
		MerchantID:        claims.MerchantID, // always from the token (C-6.2)
		MerchantReference: q.Get("merchantReference"),
		Status:            q.Get("status"),
		Limit:             atoiOr(q.Get("limit"), 50),
		Offset:            atoiOr(q.Get("offset"), 0),
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"query parameter from=%q is not an RFC3339 timestamp", v)
		}
		p.From = &t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"query parameter to=%q is not an RFC3339 timestamp", v)
		}
		p.To = &t
	}

	items, total, err := a.st.SearchCollections(r.Context(), p)
	if err != nil {
		return err
	}
	redacted := make([]store.Collection, 0, len(items))
	for _, c := range items {
		redacted = append(redacted, c.Redact(claims.Role))
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"items": redacted, "total": total, "limit": p.Limit, "offset": p.Offset,
	})
}

func (a *API) getMerchant(w http.ResponseWriter, r *http.Request) error {
	m, err := a.resolveMerchant(r.Context(), claimsFrom(r.Context()).MerchantID)
	if err != nil {
		return err
	}
	// S-5.1 / RES-7: the residency statement is derived from the merchant's stored
	// data region, so it is true by construction rather than by policy.
	country := map[string]string{"KE": "Kenya", "NG": "Nigeria"}[m.DataRegion]
	return writeJSON(w, http.StatusOK, map[string]any{
		"merchant":            m,
		"dataRegion":          m.DataRegion,
		"dataRegionStatement": "Your transaction data is stored in " + country + ".",
	})
}

func (a *API) getBalance(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	merchantID := claimsFrom(ctx).MerchantID

	m, err := a.resolveMerchant(ctx, merchantID)
	if err != nil {
		return err
	}
	bal, err := a.ledger.Balance(ctx, merchantID)
	if err != nil {
		return platform.Errorf(http.StatusServiceUnavailable, platform.CodeUpstreamUnavailable,
			"cannot read merchant %s unsettled balance from the ledger: %v", merchantID, err)
	}

	// C-1.11: the balance and the limit together - the console needs both to show headroom.
	return writeJSON(w, http.StatusOK, map[string]any{
		"merchantId":            merchantID,
		"unsettledBalanceMinor": int64(bal.AmountMinor),
		"floatLimitMinor":       int64(m.FloatLimitMinor),
		"headroomMinor":         int64(m.FloatLimitMinor - bal.AmountMinor),
		"currency":              m.FloatLimitCurrency,
		"asOf":                  bal.AsOf,
	})
}

func (a *API) getAggregates(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	to := time.Now().UTC()
	from := to.Add(-24 * time.Hour)

	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"query parameter from=%q is not an RFC3339 timestamp", v)
		}
		from = t.UTC()
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"query parameter to=%q is not an RFC3339 timestamp", v)
		}
		to = t.UTC()
	}

	agg, err := a.st.Aggregates(r.Context(), claimsFrom(r.Context()).MerchantID, from, to)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, agg)
}

// listRuns implements C-1.13: merchant-filtered for merchant users, unfiltered for
// operations and finance.
func (a *API) listRuns(w http.ResponseWriter, r *http.Request) error {
	claims := claimsFrom(r.Context())
	merchantFilter := claims.MerchantID
	if claims.Role.IsCrossMerchant() {
		merchantFilter = ""
	}

	runs, err := a.st.ListRuns(r.Context(), merchantFilter, a.cfg.DataRegion,
		atoiOr(r.URL.Query().Get("limit"), 50))
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"items":         runs,
		"windowSeconds": (2 * time.Hour).Seconds(), // S-3.3 / NFR-3
	})
}

func (a *API) getStatement(w http.ResponseWriter, r *http.Request) error {
	claims := claimsFrom(r.Context())
	merchantID := claims.MerchantID
	if claims.Role == platform.RoleFinance {
		// C-6.4: finance reads across merchants, but must name which one.
		merchantID = r.URL.Query().Get("merchantId")
		if merchantID == "" {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"query parameter merchantId is required for a finance-role statement request")
		}
	}

	st, err := a.st.Statement(r.Context(), merchantID, r.PathValue("month"))
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, st)
}

// runHealth is S-6: run health across all merchants, for operations.
// S-6.1: a run that failed is distinguishable from one that never started.
func (a *API) runHealth(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	runs, err := a.st.ListRuns(ctx, "", a.cfg.DataRegion, atoiOr(r.URL.Query().Get("limit"), 50))
	if err != nil {
		return err
	}
	status, err := a.st.SettlementStatus(ctx, a.cfg.DataRegion, a.cfg.StalenessThreshold)
	if err != nil {
		return err
	}

	return writeJSON(w, http.StatusOK, map[string]any{
		"runs":             runs,
		"settlementStatus": status,
		"windowSeconds":    (2 * time.Hour).Seconds(),
		// S-6.1: no completed run and no run record at all is "never started", which is a
		// different operational condition from a failed run.
		"neverStarted": status.LastCompletedRunAt == nil && len(runs) == 0,
	})
}

// settlementStatus is the signal behind the S-3.4 banner. Every screen polls it.
func (a *API) settlementStatus(w http.ResponseWriter, r *http.Request) error {
	st, err := a.st.SettlementStatus(r.Context(), a.cfg.DataRegion, a.cfg.StalenessThreshold)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, st)
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	platform.WriteJSON(w, status, body)
	return nil
}

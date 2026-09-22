package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/mopay/ledger/internal/collections"
	"github.com/mopay/ledger/internal/config"
	"github.com/mopay/ledger/internal/platform"
	"github.com/mopay/ledger/internal/store"
)

type API struct {
	cfg *config.Config
	st  *store.Store
	// cs is the collections-side datastore. It lives here because `ledger` is the only
	// component that holds a Postgres connection.
	cs  *collections.Store
	log *slog.Logger
	reg *platform.Registry
}

func New(cfg *config.Config, st *store.Store, cs *collections.Store, log *slog.Logger, reg *platform.Registry) *API {
	return &API{cfg: cfg, st: st, cs: cs, log: log, reg: reg}
}

// Routes mounts the ledger's surface.
//
// Every business route is under /internal. Hard constraint 8: `ledger` is never reachable
// from the console or from outside the project - only collections-api and
// settlement-worker call it. Under Compose that is enforced by not publishing the port;
// on the platform it is enforced by the endpoint's visibility.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", platform.Health("ledger", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), a.cfg.QueryTimeout)
		defer cancel()
		return a.st.Ping(ctx)
	}))
	mux.Handle("GET /metrics", a.reg.Handler())

	mux.HandleFunc("POST /internal/ledger/entries", platform.Handler(a.writeEntries).Serve(a.log))
	mux.HandleFunc("GET /internal/ledger/balances/{merchantId}", platform.Handler(a.balance).Serve(a.log))
	mux.HandleFunc("GET /internal/ledger/entries", platform.Handler(a.entriesByEvent).Serve(a.log))
	mux.HandleFunc("GET /internal/settlements/pending", platform.Handler(a.pendingSettlements).Serve(a.log))
	mux.HandleFunc("POST /internal/settlements/commit", platform.Handler(a.commitSettlement).Serve(a.log))

	// Everything collections-api and settlement-worker used to query directly.
	a.collectionsRoutes(mux)

	return platform.Observe(mux, a.log, a.reg, a.st)
}

// writeEntries appends a balanced group (C-3.1, C-3.5).
func (a *API) writeEntries(w http.ResponseWriter, r *http.Request) error {
	var g store.EntryGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"request body is not valid JSON: %v", err)
	}

	ctx := r.Context()
	entries, err := a.st.WriteGroup(ctx, g)
	if err != nil {
		if apiErr, ok := err.(*platform.APIError); ok && apiErr.Code == platform.CodeLedgerNotBalanced {
			// Constraint 2: the imbalance is logged with the group id and the actual sum.
			platform.LoggerFrom(ctx, a.log).Error("rejected unbalanced ledger write",
				slog.String("transaction_group_id", g.TransactionGroupID),
				slog.String("event_type", g.EventType),
				slog.String("reason", apiErr.Message))
		}
		return err
	}

	platform.LoggerFrom(ctx, a.log).Info("ledger group written",
		slog.String("transaction_group_id", g.TransactionGroupID),
		slog.String("event_type", g.EventType),
		slog.Int("entry_count", len(entries)))
	a.reg.IncCounter("ledger_entry_groups_written_total",
		"Balanced ledger entry groups written.", platform.Labels{"event_type": g.EventType})

	return writeJSON(w, http.StatusCreated, map[string]any{
		"transactionGroupId": g.TransactionGroupID,
		"entries":            entries,
	})
}

// balance computes a merchant's position, optionally as of a past timestamp (C-3.2).
func (a *API) balance(w http.ResponseWriter, r *http.Request) error {
	merchantID := r.PathValue("merchantId")
	asOf := time.Now().UTC()

	if raw := r.URL.Query().Get("asOf"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
				"query parameter asOf=%q is not an RFC3339 timestamp", raw)
		}
		asOf = parsed.UTC()
	}

	ctx := r.Context()
	exists, err := a.st.MerchantExists(ctx, merchantID)
	if err != nil {
		return err
	}
	if !exists {
		return platform.Errorf(http.StatusNotFound, platform.CodeNotFound,
			"merchant %s does not exist", merchantID)
	}

	b, err := a.st.Balance(ctx, merchantID, asOf)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, b)
}

// entriesByEvent serves the entries for one business event (C-3.4), which is what
// collection detail (C-1.9) renders.
func (a *API) entriesByEvent(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	eventType, eventID := q.Get("eventType"), q.Get("eventId")

	if eventType == "" || eventID == "" {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"query parameters eventType and eventId are both required")
	}

	entries, err := a.st.EntriesByEvent(r.Context(), eventType, eventID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// pendingSettlements is the unpaginated full-period reconciliation read.
// Hard constraint 3: no limit, no offset, no cursor, no cap. Do not add one.
func (a *API) pendingSettlements(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	region := q.Get("region")

	periodStart, err := requiredTime(q.Get("periodStart"), "periodStart")
	if err != nil {
		return err
	}
	periodEnd, err := requiredTime(q.Get("periodEnd"), "periodEnd")
	if err != nil {
		return err
	}

	ctx := r.Context()
	start := time.Now()
	items, err := a.st.PendingSettlements(ctx, region, periodStart, periodEnd)
	if err != nil {
		return err
	}

	platform.LoggerFrom(ctx, a.log).Info("pending settlements read",
		slog.String("region", region),
		slog.String("period_start", periodStart.Format(time.RFC3339)),
		slog.String("period_end", periodEnd.Format(time.RFC3339)),
		slog.Int("row_count", len(items)),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()))

	a.reg.SetGauge("ledger_pending_settlements_rows",
		"Rows returned by the most recent pending-settlements read.",
		platform.Labels{"region": region}, float64(len(items)))

	return writeJSON(w, http.StatusOK, map[string]any{
		"region":      region,
		"periodStart": periodStart,
		"periodEnd":   periodEnd,
		"rowCount":    len(items),
		"items":       items,
	})
}

// commitSettlement settles one merchant atomically (C-2.6, C-2.7).
func (a *API) commitSettlement(w http.ResponseWriter, r *http.Request) error {
	var req store.CommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"request body is not valid JSON: %v", err)
	}

	ctx := r.Context()
	res, err := a.st.CommitSettlement(ctx, req)
	if err != nil {
		return err
	}

	log := platform.LoggerFrom(ctx, a.log).With(
		slog.String(platform.FieldMerchant, req.MerchantID),
		slog.String("run_id", req.RunID),
		slog.String("settlement_line_id", res.Line.ID))
	if res.AlreadySettled {
		log.Info("settlement already committed for merchant in this run; no change made")
	} else {
		log.Info("settlement committed",
			slog.Int64("row_count", res.Line.RowCount),
			slog.String("payout_id", res.PayoutID))
		a.reg.IncCounter("ledger_settlement_lines_written_total",
			"Settlement lines written.", platform.Labels{"currency": string(req.Currency)})
	}

	status := http.StatusCreated
	if res.AlreadySettled {
		status = http.StatusOK
	}
	return writeJSON(w, status, res)
}

func requiredTime(raw, name string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"query parameter %s is required (RFC3339)", name)
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"query parameter %s=%q is not an RFC3339 timestamp", name, raw)
	}
	return t.UTC(), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	platform.WriteJSON(w, status, body)
	return nil
}

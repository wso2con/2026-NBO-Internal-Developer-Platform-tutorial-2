package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mopay/ledger/internal/collections"
	"github.com/mopay/ledger/internal/platform"
)

// The collections data surface.
//
// `ledger` is the only component that holds a Postgres connection, so every query
// collections-api and settlement-worker used to run themselves arrives here instead.
//
// This is an internal RPC surface, not a REST API: uniform POST-with-JSON-body, one
// endpoint per store method, named after the method rather than after a resource. It is
// under /internal like everything else in this service and is never exposed outside the
// project (hard constraint 8).

func decode[T any](r *http.Request) (T, error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return v, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"request body is not valid JSON: %v", err)
	}
	return v, nil
}

func (a *API) collectionsRoutes(mux *http.ServeMux) {
	h := func(f platform.Handler) http.HandlerFunc { return platform.Handler(f).Serve(a.log) }

	mux.HandleFunc("POST /internal/collections/merchant", h(a.cMerchant))
	mux.HandleFunc("POST /internal/collections/project-merchant", h(a.cProjectMerchant))
	mux.HandleFunc("POST /internal/collections/create", h(a.cCreate))
	mux.HandleFunc("POST /internal/collections/by-reference", h(a.cByReference))
	mux.HandleFunc("POST /internal/collections/get", h(a.cGet))
	mux.HandleFunc("POST /internal/collections/callback", h(a.cCallback))
	mux.HandleFunc("POST /internal/collections/search", h(a.cSearch))
	mux.HandleFunc("POST /internal/collections/aggregates", h(a.cAggregates))
	mux.HandleFunc("POST /internal/collections/runs", h(a.cListRuns))
	mux.HandleFunc("POST /internal/collections/statement", h(a.cStatement))
	mux.HandleFunc("POST /internal/collections/settlement-status", h(a.cSettlementStatus))

	mux.HandleFunc("POST /internal/settlements/runs/insert", h(a.cInsertRun))
	mux.HandleFunc("POST /internal/settlements/runs/finish", h(a.cFinishRun))
	mux.HandleFunc("POST /internal/settlements/statements", h(a.cMonthlyStatements))
}

type idReq struct {
	ID string `json:"id"`
}

func (a *API) cMerchant(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[idReq](r)
	if err != nil {
		return err
	}
	m, err := a.cs.Merchant(r.Context(), req.ID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, m)
}

func (a *API) cProjectMerchant(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[collections.ProjectedMerchant](r)
	if err != nil {
		return err
	}
	if err := a.cs.UpsertMerchantProjection(r.Context(), req); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) cCreate(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[collections.CreateCollectionRequest](r)
	if err != nil {
		return err
	}
	col, existed, err := a.cs.CreateCollection(r.Context(), req)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"collection": col, "existed": existed})
}

type byRefReq struct {
	MerchantID string `json:"merchantId"`
	Reference  string `json:"reference"`
}

func (a *API) cByReference(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[byRefReq](r)
	if err != nil {
		return err
	}
	col, err := a.cs.CollectionByMerchantReference(r.Context(), req.MerchantID, req.Reference)
	if err != nil {
		return err
	}
	// A miss is not an error here - C-1.3 asks "has this reference been seen", and "no"
	// is a valid answer. null is that answer.
	return writeJSON(w, http.StatusOK, map[string]any{"collection": col})
}

func (a *API) cGet(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[idReq](r)
	if err != nil {
		return err
	}
	col, err := a.cs.Collection(r.Context(), req.ID)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"collection": col})
}

type callbackReq struct {
	CollectionID string    `json:"collectionId"`
	Outcome      string    `json:"outcome"`
	At           time.Time `json:"at"`
}

func (a *API) cCallback(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[callbackReq](r)
	if err != nil {
		return err
	}
	col, changed, err := a.cs.ApplyCallback(r.Context(), req.CollectionID, req.Outcome, req.At)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"collection": col, "changed": changed})
}

func (a *API) cSearch(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[collections.SearchParams](r)
	if err != nil {
		return err
	}
	items, total, err := a.cs.SearchCollections(r.Context(), req)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

type aggregatesReq struct {
	MerchantID string    `json:"merchantId"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
}

func (a *API) cAggregates(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[aggregatesReq](r)
	if err != nil {
		return err
	}
	agg, err := a.cs.Aggregates(r.Context(), req.MerchantID, req.From, req.To)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, agg)
}

type listRunsReq struct {
	MerchantID string `json:"merchantId"`
	Region     string `json:"region"`
	Limit      int    `json:"limit"`
}

func (a *API) cListRuns(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[listRunsReq](r)
	if err != nil {
		return err
	}
	runs, err := a.cs.ListRuns(r.Context(), req.MerchantID, req.Region, req.Limit)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

type statementReq struct {
	MerchantID string `json:"merchantId"`
	Month      string `json:"month"`
}

func (a *API) cStatement(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[statementReq](r)
	if err != nil {
		return err
	}
	st, err := a.cs.Statement(r.Context(), req.MerchantID, req.Month)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, st)
}

type settlementStatusReq struct {
	Region           string `json:"region"`
	StalenessSeconds int64  `json:"stalenessSeconds"`
}

func (a *API) cSettlementStatus(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[settlementStatusReq](r)
	if err != nil {
		return err
	}
	st, err := a.cs.SettlementStatus(r.Context(), req.Region,
		time.Duration(req.StalenessSeconds)*time.Second)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, st)
}

func (a *API) cInsertRun(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[collections.InsertRunRequest](r)
	if err != nil {
		return err
	}
	if err := a.cs.InsertRun(r.Context(), req); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) cFinishRun(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[collections.FinishRunRequest](r)
	if err != nil {
		return err
	}
	if err := a.cs.FinishRun(r.Context(), req); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type monthlyStatementsReq struct {
	Region      string    `json:"region"`
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
}

func (a *API) cMonthlyStatements(w http.ResponseWriter, r *http.Request) error {
	req, err := decode[monthlyStatementsReq](r)
	if err != nil {
		return err
	}
	out, err := a.cs.MonthlyStatements(r.Context(), req.Region, req.PeriodStart, req.PeriodEnd)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"statements": out})
}

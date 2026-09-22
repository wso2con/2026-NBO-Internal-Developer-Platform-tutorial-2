package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mopay/collections-api/internal/config"
	"github.com/mopay/collections-api/internal/ledgerclient"
	"github.com/mopay/collections-api/internal/merchantclient"
	"github.com/mopay/collections-api/internal/platform"
	"github.com/mopay/collections-api/internal/store"
)

type API struct {
	cfg      *config.Config
	st       *store.Store
	ledger   *ledgerclient.Client
	merchant *merchantclient.Client
	log      *slog.Logger
	reg      *platform.Registry
}

func New(cfg *config.Config, st *store.Store, lc *ledgerclient.Client, mc *merchantclient.Client, log *slog.Logger, reg *platform.Registry) *API {
	return &API{cfg: cfg, st: st, ledger: lc, merchant: mc, log: log, reg: reg}
}

type claimsKey struct{}

func claimsFrom(ctx context.Context) platform.Claims {
	c, _ := ctx.Value(claimsKey{}).(platform.Claims)
	return c
}

// authenticated wraps a handler with dev-token parsing and the role check.
//
// C-6.6: authorization is enforced here, at the API layer. The console hiding a screen
// is not a control, and every one of these checks holds with the console removed.
func (a *API) authenticated(allowed []platform.Role, h platform.Handler) http.HandlerFunc {
	return platform.Handler(func(w http.ResponseWriter, r *http.Request) error {
		claims, err := platform.ClaimsFromRequest(r)
		if err != nil {
			return platform.Errorf(http.StatusUnauthorized, platform.CodeUnauthorized,
				"request is not authenticated: %v", err)
		}

		permitted := false
		for _, role := range allowed {
			if claims.Role == role {
				permitted = true
				break
			}
		}
		if !permitted {
			// C-6.7: every authorization denial is logged with subject, action and resource.
			platform.LoggerFrom(r.Context(), a.log).Warn("authorization denied",
				slog.String("subject", claims.Subject),
				slog.String("role", string(claims.Role)),
				slog.String("action", r.Method),
				slog.String("resource", r.URL.Path))
			a.reg.IncCounter("authz_denials_total", "Authorization denials.",
				platform.Labels{"role": string(claims.Role), "route": r.Pattern})
			return platform.Errorf(http.StatusForbidden, platform.CodeForbidden,
				"role %q may not %s %s", claims.Role, r.Method, r.URL.Path)
		}

		ctx := context.WithValue(r.Context(), claimsKey{}, claims)
		if claims.MerchantID != "" {
			ctx = platform.WithLogger(ctx,
				platform.LoggerFrom(ctx, a.log).With(slog.String(platform.FieldMerchant, claims.MerchantID)))
		}
		return h(w, r.WithContext(ctx))
	}).Serve(a.log)
}

var (
	merchantOnly    = []platform.Role{platform.RoleMerchant}
	merchantService = []platform.Role{platform.RoleMerchant, platform.RoleService}
	merchantFinance = []platform.Role{platform.RoleMerchant, platform.RoleFinance}
	operationsOnly  = []platform.Role{platform.RoleOperations}
	anyRole         = []platform.Role{platform.RoleMerchant, platform.RoleService, platform.RoleFinance, platform.RoleOperations}
)

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", platform.Health("collections-api", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), a.cfg.QueryTimeout)
		defer cancel()
		return a.st.Ping(ctx)
	}))
	mux.Handle("GET /metrics", a.reg.Handler())

	// Intake (§5.1)
	mux.HandleFunc("POST /v1/collections", a.authenticated(merchantService, a.createCollection))
	// Channel callbacks are unauthenticated in dev mode: a real channel posts these.
	// SCOPE.md conflict 2 - signature verification is a README.md non-goal.
	mux.HandleFunc("POST /v1/channel-callbacks", platform.Handler(a.channelCallback).Serve(a.log))

	// Reads (§5.2)
	mux.HandleFunc("GET /v1/collections", a.authenticated(merchantService, a.searchCollections))
	mux.HandleFunc("GET /v1/collections/{id}", a.authenticated(merchantService, a.getCollection))
	mux.HandleFunc("GET /v1/merchants/me", a.authenticated(merchantOnly, a.getMerchant))
	mux.HandleFunc("GET /v1/merchants/me/balance", a.authenticated(merchantService, a.getBalance))
	mux.HandleFunc("GET /v1/aggregates", a.authenticated(merchantOnly, a.getAggregates))
	mux.HandleFunc("GET /v1/settlement-runs", a.authenticated(anyRole, a.listRuns))
	mux.HandleFunc("GET /v1/statements/{month}", a.authenticated(merchantFinance, a.getStatement))

	// Operations (§6 S-6) and the S-3.4 banner signal.
	mux.HandleFunc("GET /v1/ops/run-health", a.authenticated(operationsOnly, a.runHealth))
	mux.HandleFunc("GET /v1/settlement-status", a.authenticated(anyRole, a.settlementStatus))

	return a.withCORS(platform.Observe(mux, a.log, a.reg))
}

// withCORS permits the console's origin to call this API from a browser.
//
// The allowed origins are configuration. An origin that is not on the list simply gets
// no CORS headers - the browser then blocks it, which is the intended outcome. This is a
// browser-side control only and is not a substitute for C-6.2's server-side scoping.
func (a *API) withCORS(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	wildcard := false
	for _, o := range a.cfg.CORSAllowedOrigins {
		if o == "*" {
			wildcard = true
		}
		allowed[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (wildcard || allowed[origin]) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, traceparent")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "traceparent")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RefreshLag recomputes settlement lag and publishes it as a metric.
//
// HARD CONSTRAINT 4: this runs in collections-api on a short interval, NOT in
// settlement-worker, and it must keep being emitted when the worker is not running.
// A lag signal that stops when the worker dies cannot tell you the worker died.
func (a *API) RefreshLag(ctx context.Context) {
	st, err := a.st.SettlementStatus(ctx, a.cfg.DataRegion, a.cfg.StalenessThreshold)
	if err != nil {
		a.log.Error("settlement lag refresh failed; the lag metric is now stale",
			slog.String("region", a.cfg.DataRegion),
			slog.String("error", err.Error()))
		a.reg.IncCounter("settlement_lag_refresh_errors_total",
			"Failed settlement lag refreshes.", platform.Labels{"region": a.cfg.DataRegion})
		return
	}

	labels := platform.Labels{"region": a.cfg.DataRegion}
	a.reg.SetGauge("mopay_settlement_lag_seconds",
		"Age in seconds of the oldest cleared, unsettled collection (OBS-4).", labels, st.LagSeconds)
	a.reg.SetGauge("mopay_unsettled_collections",
		"Cleared collections awaiting settlement.", labels, float64(st.UnsettledCount))
	a.reg.SetGauge("mopay_unsettled_merchants",
		"Merchants with cleared, unsettled collections (C-5.4 affected merchants).",
		labels, float64(st.AffectedMerchants))

	stale := 0.0
	if st.Stale {
		stale = 1
	}
	a.reg.SetGauge("mopay_settlement_stale",
		"1 when the most recent completed run is older than the configured threshold (C-5.3).",
		labels, stale)

	// C-5.2 settlement-lag-critical. Under Compose the alert is a structured log line and
	// a metric; the platform phases wire it to the observability plane.
	if st.LagSeconds > a.cfg.LagAlertThreshold.Seconds() {
		a.reg.SetGauge("mopay_settlement_lag_critical",
			"1 when settlement lag exceeds the configured alert threshold (C-5.2).", labels, 1)

		lastRun := "none"
		if st.LastCompletedRunID != nil {
			lastRun = *st.LastCompletedRunID
		}
		// C-5.4: the alert carries the run id where applicable and the number of
		// affected merchants. The environment is the platform's label, not ours.
		a.log.Error("alert settlement-lag-critical: settlement lag exceeds threshold",
			slog.String("alert", "settlement-lag-critical"),
			slog.String("region", a.cfg.DataRegion),
			slog.String("last_completed_run_id", lastRun),
			slog.Int64("lag_seconds", int64(st.LagSeconds)),
			slog.Int64("threshold_seconds", int64(a.cfg.LagAlertThreshold.Seconds())),
			slog.Int64("affected_merchants", st.AffectedMerchants),
			slog.Int64("unsettled_collections", st.UnsettledCount))
	} else {
		a.reg.SetGauge("mopay_settlement_lag_critical",
			"1 when settlement lag exceeds the configured alert threshold (C-5.2).", labels, 0)
	}
}

// StartLagLoop runs RefreshLag until the context is cancelled.
func (a *API) StartLagLoop(ctx context.Context) {
	a.RefreshLag(ctx)
	t := time.NewTicker(a.cfg.LagRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.RefreshLag(ctx)
		}
	}
}

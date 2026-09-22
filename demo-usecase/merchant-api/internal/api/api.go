// Package api is merchant-api's HTTP surface. Unlike ledger, this service is meant to be
// FOUND: it is an exposed endpoint, described by api/merchants.openapi.yaml, and other
// projects are expected to discover and call it.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/mopay/merchant-api/internal/config"
	"github.com/mopay/merchant-api/internal/platform"
	"github.com/mopay/merchant-api/internal/store"
)

type API struct {
	cfg *config.Config
	st  *store.Store
	log *slog.Logger
	reg *platform.Registry
}

func New(cfg *config.Config, st *store.Store, log *slog.Logger, reg *platform.Registry) *API {
	return &API{cfg: cfg, st: st, log: log, reg: reg}
}

type claimsKey struct{}

func claimsFrom(ctx context.Context) platform.Claims {
	c, _ := ctx.Value(claimsKey{}).(platform.Claims)
	return c
}

var (
	// Onboarding creates a merchant, so it is a back-office act, not something a
	// merchant does to itself.
	operationsOnly = []platform.Role{platform.RoleOperations}
	// Listing reads across merchants, so only the roles the platform treats as
	// cross-merchant may do it. `service` is deliberately NOT one of them - a service
	// token is scoped to the merchant it is acting for.
	crossMerchant = []platform.Role{platform.RoleOperations, platform.RoleFinance}
	// Any role may fetch a single merchant; the handler enforces WHICH one.
	anyRole = []platform.Role{platform.RoleMerchant, platform.RoleService, platform.RoleFinance, platform.RoleOperations}
)

// authenticated wraps a handler with dev-token parsing and the role check. Authorization
// is enforced here, at the API layer - a caller that hides a button is not a control.
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
			// Every authorization denial is logged with subject, action and resource.
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

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", platform.Health("merchant-api", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), a.cfg.QueryTimeout)
		defer cancel()
		return a.st.Ping(ctx)
	}))
	mux.Handle("GET /metrics", a.reg.Handler())

	mux.HandleFunc("POST /v1/merchants", a.authenticated(operationsOnly, a.onboardMerchant))
	mux.HandleFunc("GET /v1/merchants", a.authenticated(crossMerchant, a.listMerchants))
	mux.HandleFunc("GET /v1/merchants/{merchantId}", a.authenticated(anyRole, a.getMerchant))

	return a.withCORS(platform.Observe(mux, a.log, a.reg))
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	platform.WriteJSON(w, status, body)
	return nil
}

// onboardMerchant creates a merchant. This is the only way one comes into existence on
// the platform.
func (a *API) onboardMerchant(w http.ResponseWriter, r *http.Request) error {
	var body store.OnboardRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"request body is not valid JSON: %v", err)
	}

	m, err := a.st.Onboard(r.Context(), body)
	if err != nil {
		return err
	}

	platform.LoggerFrom(r.Context(), a.log).Info("merchant onboarded",
		slog.String(platform.FieldMerchant, m.ID),
		slog.String("data_region", m.DataRegion),
		slog.String("country", m.Country))
	a.reg.IncCounter("merchants_onboarded_total", "Merchants created.",
		platform.Labels{"data_region": m.DataRegion})

	w.Header().Set("Location", "/v1/merchants/"+m.ID)
	return writeJSON(w, http.StatusCreated, m)
}

// getMerchant returns one merchant. This is the operation collections-api calls on the
// intake path, so it stays a single round trip.
func (a *API) getMerchant(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("merchantId")
	claims := claimsFrom(r.Context())

	// A merchant-scoped token - `merchant` or `service` - may read only its own record.
	// Returning 404 rather than 403 avoids confirming that another merchant's id exists.
	// This is the same rule collections-api applies in C-6.2.
	if !claims.Role.IsCrossMerchant() && claims.MerchantID != id {
		platform.LoggerFrom(r.Context(), a.log).Warn("authorization denied",
			slog.String("subject", claims.Subject),
			slog.String("role", string(claims.Role)),
			slog.String("action", r.Method),
			slog.String("resource", r.URL.Path))
		return platform.Errorf(http.StatusNotFound, platform.CodeNotFound,
			"merchant %s does not exist", id)
	}

	m, err := a.st.Get(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, m)
}

func (a *API) listMerchants(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()

	region := q.Get("dataRegion")
	if region != "" && region != "KE" && region != "NG" {
		return platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"dataRegion %q is not supported (want KE or NG)", region)
	}

	limit, err := intParam(q.Get("limit"), 50, 1, 200)
	if err != nil {
		return err
	}
	offset, err := intParam(q.Get("offset"), 0, 0, 1<<30)
	if err != nil {
		return err
	}

	items, total, err := a.st.List(r.Context(), region, limit, offset)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

func intParam(raw string, def, min, max int) (int, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"value %q is not an integer", raw)
	}
	if n < min || n > max {
		return 0, platform.Errorf(http.StatusBadRequest, platform.CodeBadRequest,
			"value %d is out of range (want %d..%d)", n, min, max)
	}
	return n, nil
}

// withCORS permits the configured browser origins.
//
// The allowed origins are configuration. An origin that is not on the list simply gets no
// CORS headers - the browser then blocks it, which is the intended outcome. This is a
// browser-side control only and is not a substitute for the server-side role and scoping
// checks above.
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

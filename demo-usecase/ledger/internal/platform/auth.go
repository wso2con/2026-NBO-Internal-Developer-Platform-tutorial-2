package platform

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Dev-mode bearer tokens. README.md lists OIDC and identity providers as explicit non-goals;
// SCOPE.md conflict 5 records the substitution. The token is an unsigned, base64url-encoded
// claims blob prefixed "dev." - deliberately not a JWT, so nobody mistakes it for one.
//
//	Authorization: Bearer dev.<base64url({"sub":"...","merchantId":"...","role":"..."})>
//
// Authorization itself (C-6.2 - C-6.7) is enforced server-side and is NOT dev-mode.

type Role string

const (
	RoleMerchant   Role = "merchant"
	RoleFinance    Role = "finance"
	RoleOperations Role = "operations"
	RoleService    Role = "service"
)

// CanSeeCustomerDetail reports whether a role may be shown customer references or
// per-transaction amounts. C-6.3: operations must not. C-6.4: finance must not.
func (r Role) CanSeeCustomerDetail() bool {
	return r == RoleMerchant || r == RoleService
}

// IsCrossMerchant reports whether a role reads across merchants rather than being
// scoped to one (C-6.2).
func (r Role) IsCrossMerchant() bool {
	return r == RoleFinance || r == RoleOperations
}

type Claims struct {
	Subject    string `json:"sub"`
	MerchantID string `json:"merchantId"`
	Role       Role   `json:"role"`
}

const devTokenPrefix = "dev."

// EncodeDevToken builds a token. Used by the seeder and the console's role picker.
func EncodeDevToken(c Claims) string {
	b, _ := json.Marshal(c)
	return devTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseAuthorization extracts claims from an Authorization header.
// Errors name the specific condition, per the README's failure-message convention.
func ParseAuthorization(header string) (Claims, error) {
	var c Claims

	header = strings.TrimSpace(header)
	if header == "" {
		return c, fmt.Errorf("authorization header is absent")
	}
	const bearer = "bearer "
	if len(header) < len(bearer) || !strings.EqualFold(header[:len(bearer)], bearer) {
		return c, fmt.Errorf("authorization header is not a Bearer token")
	}
	token := strings.TrimSpace(header[len(bearer):])
	if !strings.HasPrefix(token, devTokenPrefix) {
		return c, fmt.Errorf("bearer token is not a dev-mode token (expected %q prefix)", devTokenPrefix)
	}

	raw, err := base64.RawURLEncoding.DecodeString(token[len(devTokenPrefix):])
	if err != nil {
		return c, fmt.Errorf("dev token payload is not valid base64url: %w", err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("dev token payload is not valid JSON: %w", err)
	}

	if c.Subject == "" {
		return c, fmt.Errorf("dev token is missing claim \"sub\"")
	}
	switch c.Role {
	case RoleMerchant, RoleFinance, RoleOperations, RoleService:
	case "":
		return c, fmt.Errorf("dev token is missing claim \"role\"")
	default:
		return c, fmt.Errorf("dev token carries unknown role %q (want merchant, finance, operations or service)", c.Role)
	}
	// A merchant-scoped role without a merchant id cannot be scoped server-side (C-6.2),
	// so refuse it rather than defaulting to "all merchants".
	if !c.Role.IsCrossMerchant() && c.MerchantID == "" {
		return c, fmt.Errorf("dev token with role %q is missing claim \"merchantId\"", c.Role)
	}
	return c, nil
}

// ClaimsFromRequest is the per-request entry point.
func ClaimsFromRequest(r *http.Request) (Claims, error) {
	return ParseAuthorization(r.Header.Get("Authorization"))
}

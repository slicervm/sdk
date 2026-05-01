package slicer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ProxyClient holds an opaque token used to authenticate against the
// slicer-proxy data plane via HTTPS_PROXY=https://:<token>@host:port.
type ProxyClient struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// ProxyClientCreated is the response shape from CreateProxyClient. The
// minted Token is shown once at create time and never returned by any
// other endpoint.
type ProxyClientCreated struct {
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// Secret types. Empty value is treated as bearer for backwards compat
// with state files / API callers that predate the field.
const (
	SecretTypeBearer             = "bearer"
	SecretTypeBasic              = "basic"
	SecretTypeOAuthCodex         = "oauth-codex"
	SecretTypeOAuthClaude        = "oauth-claude"
	SecretTypeOAuthGitHubCopilot = "oauth-github-copilot"
)

// ProxySecret is an upstream credential the proxy injects into matching
// CONNECT requests. List/get endpoints never return Value.
type ProxySecret struct {
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Type        string    `json:"type,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
	AdoptedAt   time.Time `json:"adopted_at,omitempty"`
	RefreshedAt time.Time `json:"refreshed_at,omitempty"`
}

// CreateProxySecretRequest is the input to CreateProxySecret. Type
// defaults to SecretTypeBearer when empty. For SecretTypeBasic the
// Value must be in user:pass form (the proxy base64-encodes it). For
// SecretTypeOAuthCodex the Value must be a Codex auth.json or minimal
// OAuth JSON containing access_token, refresh_token, and account_id;
// the proxy adopts it and refreshes it from host-side state.
// For SecretTypeOAuthClaude the Value must be Claude Code's
// .credentials.json or minimal OAuth JSON containing accessToken and
// refreshToken; the proxy adopts it and refreshes it from host-side state.
// For SecretTypeOAuthGitHubCopilot the Value must be opencode auth.json
// or minimal OAuth JSON containing a GitHub Copilot ghu_* token; the
// proxy exchanges it for short-lived Copilot session tokens and stores
// the returned API endpoint in host-side state.
type CreateProxySecretRequest struct {
	Name  string `json:"name"`
	Host  string `json:"host"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value"`
	Force bool   `json:"force,omitempty"`
}

// ProxyAllowRule grants a ProxyClient access to one host (exact,
// *.suffix wildcard, or "*" for any). When Secret is set, the proxy
// strips the client-supplied Authorization on the inner request and
// substitutes the rule-bound credential.
//
// Methods and Paths are optional filters. When set, the request must
// match at least one entry in each non-empty list. Empty list = any.
// Path supports exact match, "*" (any path), or "<prefix>/*" suffix
// glob (anything strictly under the prefix).
//
// Ports is optional. Empty means the default web ports 80 and 443 for
// backwards compatibility with older host-only rules. When set, the
// request must target one of the listed upstream ports.
//
// When Passthrough is true the proxy splices TCP both ways at CONNECT
// without terminating TLS — no MITM, no inner-request inspection.
// The guest does not need to trust the proxy CA on a passthrough
// host, so cert-pinned clients work. Passthrough is mutually
// exclusive with Secret, Methods, and Paths; the admin API rejects
// rules that combine them.
type ProxyAllowRule struct {
	Host        string    `json:"host"`
	Secret      string    `json:"secret,omitempty"`
	Methods     []string  `json:"methods,omitempty"`
	Paths       []string  `json:"paths,omitempty"`
	Ports       []int     `json:"ports,omitempty"`
	Expires     time.Time `json:"expires,omitempty"`
	Passthrough bool      `json:"passthrough,omitempty"`
}

// RemoveProxyAllowByTupleRequest mirrors the create payload (minus
// TTLSeconds, which is not part of identity). The proxy matches the
// rule by (host, methods, paths, ports, passthrough) and removes it from
// the client's allow list. Useful when the caller owns the tuple —
// e.g. a declarative config — and wants surgical removal of one
// rule among siblings on the same host.
type RemoveProxyAllowByTupleRequest struct {
	Client      string   `json:"client"`
	Host        string   `json:"host"`
	Secret      string   `json:"secret,omitempty"`
	Methods     []string `json:"methods,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Ports       []int    `json:"ports,omitempty"`
	Passthrough bool     `json:"passthrough,omitempty"`
}

// AddProxyAllowRequest is the input to AddProxyAllow.
type AddProxyAllowRequest struct {
	Client      string   `json:"client"`
	Host        string   `json:"host"`
	Secret      string   `json:"secret,omitempty"`
	Methods     []string `json:"methods,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Ports       []int    `json:"ports,omitempty"`
	TTLSeconds  int      `json:"ttl_seconds,omitempty"`
	Passthrough bool     `json:"passthrough,omitempty"`
}

// CreateProxyClient mints a new proxy access token. setToken lets the
// caller bring their own value (handy for demos / reproducible tests);
// pass "" for a server-minted token (recommended). The returned Token
// is shown once and is not retrievable later.
func (c *SlicerClient) CreateProxyClient(ctx context.Context, name, setToken string) (*ProxyClientCreated, error) {
	body := map[string]any{"name": name}
	if setToken != "" {
		body["token"] = setToken
	}
	var out ProxyClientCreated
	if err := c.proxyDo(ctx, http.MethodPost, "/proxy/v1/clients", body, http.StatusCreated, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListProxyClients returns all registered proxy clients (no tokens).
func (c *SlicerClient) ListProxyClients(ctx context.Context) ([]ProxyClient, error) {
	var out []ProxyClient
	return out, c.proxyDo(ctx, http.MethodGet, "/proxy/v1/clients", nil, http.StatusOK, &out)
}

// DeleteProxyClient revokes the token, drops every allow rule the
// client owned, and removes the client.
func (c *SlicerClient) DeleteProxyClient(ctx context.Context, name string) error {
	return c.proxyDo(ctx, http.MethodDelete, "/proxy/v1/clients/"+name, nil, http.StatusNoContent, nil)
}

// CreateProxySecret registers an upstream credential the proxy can
// inject when an allow rule references it.
func (c *SlicerClient) CreateProxySecret(ctx context.Context, req CreateProxySecretRequest) error {
	return c.proxyDo(ctx, http.MethodPost, "/proxy/v1/secrets", req, http.StatusCreated, nil)
}

// ListProxySecrets returns all registered secrets (Value field is never
// returned by the server).
func (c *SlicerClient) ListProxySecrets(ctx context.Context) ([]ProxySecret, error) {
	var out []ProxySecret
	return out, c.proxyDo(ctx, http.MethodGet, "/proxy/v1/secrets", nil, http.StatusOK, &out)
}

// DeleteProxySecret removes a secret. Allow rules that reference it
// stop matching until the secret is recreated or the rule is rewritten.
func (c *SlicerClient) DeleteProxySecret(ctx context.Context, name string) error {
	return c.proxyDo(ctx, http.MethodDelete, "/proxy/v1/secrets/"+name, nil, http.StatusNoContent, nil)
}

// AddProxyAllow grants a client access to a host, optionally injecting
// a named secret on every CONNECT to that host.
func (c *SlicerClient) AddProxyAllow(ctx context.Context, req AddProxyAllowRequest) error {
	return c.proxyDo(ctx, http.MethodPost, "/proxy/v1/allows", req, http.StatusCreated, nil)
}

// RemoveProxyAllow removes every allow rule for a client whose host
// matches. This is the host-bulk verb. For surgical removal of a
// single rule (when several share a host but differ on paths /
// methods / passthrough) use RemoveProxyAllowByTuple.
func (c *SlicerClient) RemoveProxyAllow(ctx context.Context, client, host string) error {
	return c.proxyDo(ctx, http.MethodDelete, "/proxy/v1/allows/"+client+"/"+host, nil, http.StatusNoContent, nil)
}

// RemoveProxyAllowByTuple removes the single allow rule whose
// (host, methods, paths, passthrough) tuple matches the request.
// Callers pass the same fields they used to create the rule; TTL is
// not part of identity and is omitted from the request type.
func (c *SlicerClient) RemoveProxyAllowByTuple(ctx context.Context, req RemoveProxyAllowByTupleRequest) error {
	return c.proxyDo(ctx, http.MethodPost, "/proxy/v1/allows/revoke", req, http.StatusNoContent, nil)
}

// ListProxyRules returns the client's allow rules in declaration order.
func (c *SlicerClient) ListProxyRules(ctx context.Context, client string) ([]ProxyAllowRule, error) {
	var out []ProxyAllowRule
	return out, c.proxyDo(ctx, http.MethodGet, "/proxy/v1/clients/"+client, nil, http.StatusOK, &out)
}

// proxyDo wraps makeJSONRequestWithContext with a status check and an
// optional JSON decode. Non-matching status codes surface the server's
// response body as the error message so callers can see e.g.
// "client already exists".
func (c *SlicerClient) proxyDo(ctx context.Context, method, path string, body any, wantStatus int, out any) error {
	resp, err := c.makeJSONRequestWithContext(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

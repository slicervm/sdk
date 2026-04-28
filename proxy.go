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
	SecretTypeBearer = "bearer"
	SecretTypeBasic  = "basic"
)

// ProxySecret is an upstream credential the proxy injects into matching
// CONNECT requests. List/get endpoints never return Value.
type ProxySecret struct {
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Type      string    `json:"type,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateProxySecretRequest is the input to CreateProxySecret. Type
// defaults to SecretTypeBearer when empty. For SecretTypeBasic the
// Value must be in user:pass form (the proxy base64-encodes it).
type CreateProxySecretRequest struct {
	Name  string `json:"name"`
	Host  string `json:"host"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value"`
}

// ProxyAllowRule grants a ProxyClient access to one host (exact, *.suffix
// wildcard, or "*" for any). When Secret is set, the proxy strips the
// client-supplied Authorization on the inner request and substitutes
// "Bearer <secret-value>".
type ProxyAllowRule struct {
	Host    string    `json:"host"`
	Secret  string    `json:"secret,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
}

// AddProxyAllowRequest is the input to AddProxyAllow.
type AddProxyAllowRequest struct {
	Client     string `json:"client"`
	Host       string `json:"host"`
	Secret     string `json:"secret,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
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

// RemoveProxyAllow revokes one allow rule for a client.
func (c *SlicerClient) RemoveProxyAllow(ctx context.Context, client, host string) error {
	return c.proxyDo(ctx, http.MethodDelete, "/proxy/v1/allows/"+client+"/"+host, nil, http.StatusNoContent, nil)
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

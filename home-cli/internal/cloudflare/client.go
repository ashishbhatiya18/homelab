// Package cloudflare is a small client for the parts of the Cloudflare v4 API
// `home cloud` uses: accounts, zones, DNS records and Cloudflare Tunnels
// (their remote ingress configuration and live connectors). The token is an
// API token kept in the Keychain; nothing here stores it.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultBase = "https://api.cloudflare.com/client/v4"

type Client struct {
	Token string
	Base  string // DefaultBase when empty; tests point it at httptest
	HTTP  *http.Client
}

func New(token string) *Client {
	return &Client{Token: token, Base: DefaultBase, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// APIError is an unsuccessful API response.
type APIError struct {
	Status int
	Path   string
	Errors []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
}

func (e *APIError) Error() string {
	var msgs []string
	for _, m := range e.Errors {
		msgs = append(msgs, fmt.Sprintf("%s (%d)", m.Message, m.Code))
	}
	if len(msgs) == 0 {
		msgs = append(msgs, http.StatusText(e.Status))
	}
	return fmt.Sprintf("cloudflare %s: %d %s", e.Path, e.Status, strings.Join(msgs, "; "))
}

type envelope struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Info    *struct {
		Page       int `json:"page"`
		PerPage    int `json:"per_page"`
		TotalPages int `json:"total_pages"`
		Count      int `json:"count"`
	} `json:"result_info"`
	Errors []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) (*envelope, error) {
	base := c.Base
	if base == "" {
		base = DefaultBase
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare %s: %w", path, err)
	}
	defer resp.Body.Close()
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("cloudflare %s: %s: unreadable response: %w", path, resp.Status, err)
	}
	if !env.Success || resp.StatusCode/100 != 2 {
		return nil, &APIError{Status: resp.StatusCode, Path: path, Errors: env.Errors}
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("cloudflare %s: %w", path, err)
		}
	}
	return &env, nil
}

// list fetches every page of a paginated collection.
func list[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	const perPage = 100
	if q == nil {
		q = url.Values{}
	}
	var all []T
	for page := 1; ; page++ {
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(perPage))
		var items []T
		env, err := c.do(ctx, http.MethodGet, path, q, nil, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if env.Info == nil || len(items) == 0 {
			return all, nil
		}
		if env.Info.TotalPages > 0 && page >= env.Info.TotalPages {
			return all, nil
		}
		if env.Info.TotalPages == 0 && len(items) < perPage {
			return all, nil
		}
	}
}

// ---- accounts and zones ------------------------------------------------------

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	return list[Account](ctx, c, "/accounts", nil)
}

type Zone struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Account Account `json:"account"`
}

// Zones lists the zones of an account the token can read; with no account,
// every zone it can read.
func (c *Client) Zones(ctx context.Context, accountID string) ([]Zone, error) {
	q := url.Values{}
	if accountID != "" {
		q.Set("account.id", accountID)
	}
	return list[Zone](ctx, c, "/zones", q)
}

// VisibleAccounts returns the accounts the token can act in. /accounts needs
// the Account Settings permission and is silently empty without it, so the
// accounts owning the zones it can read count too.
func (c *Client) VisibleAccounts(ctx context.Context) ([]Account, error) {
	accounts, err := c.Accounts(ctx)
	if err != nil || len(accounts) > 0 {
		return accounts, err
	}
	zones, err := c.Zones(ctx, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, z := range zones {
		if z.Account.ID != "" && !seen[z.Account.ID] {
			seen[z.Account.ID] = true
			accounts = append(accounts, z.Account)
		}
	}
	return accounts, nil
}

// ---- DNS ---------------------------------------------------------------------

type DNSRecord struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Content  string         `json:"content"`
	Proxied  bool           `json:"proxied"`
	TTL      int            `json:"ttl"`
	Priority *int           `json:"priority,omitempty"`
	Comment  string         `json:"comment,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

// ReadOnly reports a record Cloudflare manages itself (e.g. Email Routing's
// DKIM key), which the API refuses to change.
func (r DNSRecord) ReadOnly() bool {
	v, _ := r.Meta["read_only"].(bool)
	return v
}

func (c *Client) DNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	return list[DNSRecord](ctx, c, "/zones/"+url.PathEscape(zoneID)+"/dns_records", nil)
}

// ---- tunnels -------------------------------------------------------------------

type Tunnel struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Status        string     `json:"status"`     // healthy, degraded, down, inactive
	ConfigSrc     string     `json:"config_src"` // cloudflare (remote ingress) or local
	CreatedAt     time.Time  `json:"created_at"`
	ConnsActiveAt *time.Time `json:"conns_active_at"`
	TunType       string     `json:"tun_type"`
}

// Remote reports whether the ingress rules live in Cloudflare (and so can be
// read and changed through the API) rather than in cloudflared's config file.
func (t Tunnel) Remote() bool { return t.ConfigSrc == "cloudflare" }

// Tunnels lists the account's Cloudflare Tunnels that are not deleted.
func (c *Client) Tunnels(ctx context.Context, accountID string) ([]Tunnel, error) {
	return list[Tunnel](ctx, c, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel", url.Values{"is_deleted": {"false"}})
}

// IngressRule is one ingress rule, in cloudflared's own field names. Rules
// without a hostname match everything; the last rule must be one.
type IngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Path          string         `json:"path,omitempty"`
	Service       string         `json:"service"`
	OriginRequest map[string]any `json:"originRequest,omitempty"`
}

type TunnelConfig struct {
	Ingress       []IngressRule  `json:"ingress"`
	OriginRequest map[string]any `json:"originRequest,omitempty"`
	WarpRouting   map[string]any `json:"warp-routing,omitempty"`
}

type TunnelConfiguration struct {
	Version int           `json:"version"`
	Config  *TunnelConfig `json:"config"`
}

// TunnelConfig returns a remotely managed tunnel's ingress configuration
// (Config is nil when none was ever set).
func (c *Client) TunnelConfig(ctx context.Context, accountID, tunnelID string) (*TunnelConfiguration, error) {
	var out TunnelConfiguration
	_, err := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID)+"/configurations", nil, nil, &out)
	return &out, err
}

// Connector is one running cloudflared instance and its edge connections.
type Connector struct {
	ID      string    `json:"id"`
	Arch    string    `json:"arch"`
	Version string    `json:"version"`
	RunAt   time.Time `json:"run_at"`
	Conns   []struct {
		ColoName           string    `json:"colo_name"`
		OpenedAt           time.Time `json:"opened_at"`
		OriginIP           string    `json:"origin_ip"`
		IsPendingReconnect bool      `json:"is_pending_reconnect"`
	} `json:"conns"`
}

func (c *Client) Connectors(ctx context.Context, accountID, tunnelID string) ([]Connector, error) {
	var out []Connector
	_, err := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID)+"/connections", nil, nil, &out)
	return out, err
}

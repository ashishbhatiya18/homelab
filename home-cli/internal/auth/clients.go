package auth

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ClientsHeader starts a clients.yml written by home.
const ClientsHeader = `# OpenID Connect clients, managed with ` + "`home auth client …`" + `.
# Loaded by Authelia as a second configuration file (X_AUTHELIA_CONFIG); each
# secret is the PBKDF2 digest in <id>.digest next to this file, read through
# the template filter. Changes apply on an Authelia restart.
`

// Client is one OpenID Connect client.
type Client struct {
	ID, Name   string
	Policy     string   // authorization_policy: one_factor, two_factor or a custom policy
	Redirects  []string // redirect_uris
	Scopes     []string
	PKCE       bool   // require_pkce with S256
	Public     bool   // no secret (native/SPA apps); implies PKCE
	Consent    string // consent_mode: explicit, implicit, pre-configured (1 year)
	AuthMethod string // token_endpoint_auth_method of confidential clients
}

var (
	DefaultScopes = []string{"openid", "profile", "email"}
	consentModes  = map[string]bool{"explicit": true, "implicit": true, "pre-configured": true}
	authMethods   = map[string]bool{"client_secret_basic": true, "client_secret_post": true}
)

// SecretRef is the client_secret value: the digest file, as the container
// sees it under mount, read by the template filter.
func SecretRef(mount, id string) string {
	return fmt.Sprintf(`{{ secret "%s/%s.digest" }}`, strings.TrimSuffix(mount, "/"), id)
}

// Clients is a parsed clients.yml.
type Clients struct{ d *doc }

func ParseClients(b []byte) (*Clients, error) {
	d, err := parse(b, "clients.yml")
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(strings.TrimSpace(string(b))) == 0 {
		d.root.HeadComment = strings.TrimSpace(ClientsHeader)
	}
	return &Clients{d: d}, nil
}

func (c *Clients) Bytes() []byte { return c.d.bytes() }

func (c *Clients) seq() *yaml.Node {
	oidc := child(child(c.d.root, "identity_providers"), "oidc")
	s := get(oidc, "clients")
	if s == nil || s.Kind != yaml.SequenceNode {
		s = &yaml.Node{Kind: yaml.SequenceNode}
		set(oidc, "clients", s)
	}
	return s
}

func (c *Clients) find(id string) (int, *yaml.Node) {
	for i, e := range c.seq().Content {
		if scalar(e, "client_id") == id {
			return i, e
		}
	}
	return -1, nil
}

func (c *Clients) List() []Client {
	var out []Client
	for _, e := range c.seq().Content {
		out = append(out, Client{
			ID:         scalar(e, "client_id"),
			Name:       scalar(e, "client_name"),
			Policy:     scalar(e, "authorization_policy"),
			Redirects:  values(get(e, "redirect_uris")),
			Scopes:     values(get(e, "scopes")),
			PKCE:       scalar(e, "require_pkce") == "true",
			Public:     scalar(e, "public") == "true",
			Consent:    scalar(e, "consent_mode"),
			AuthMethod: scalar(e, "token_endpoint_auth_method"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *Clients) Get(id string) (Client, bool) {
	for _, x := range c.List() {
		if x.ID == id {
			return x, true
		}
	}
	return Client{}, false
}

func checkRedirects(rs []string) error {
	if len(rs) == 0 {
		return errors.New("at least one --redirect is required")
	}
	for _, r := range rs {
		u, err := url.Parse(r)
		if err != nil || u.Scheme == "" || (u.Scheme == "http" || u.Scheme == "https") && u.Host == "" {
			return fmt.Errorf("redirect %q is not an absolute URL", r)
		}
		if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
			return fmt.Errorf("redirect %q: use https (http only for localhost)", r)
		}
	}
	return nil
}

func (x *Client) check() error {
	if err := ValidName(x.ID); err != nil {
		return err
	}
	if err := checkRedirects(x.Redirects); err != nil {
		return err
	}
	if x.Consent != "" && !consentModes[x.Consent] {
		return fmt.Errorf("consent %q: use explicit, implicit or pre-configured", x.Consent)
	}
	if x.AuthMethod != "" && !authMethods[x.AuthMethod] {
		return fmt.Errorf("auth method %q: use client_secret_basic or client_secret_post", x.AuthMethod)
	}
	return nil
}

// Add appends a client; mount is where the container sees the digest files.
func (c *Clients) Add(x Client, mount string) error {
	if x.Name == "" {
		x.Name = x.ID
	}
	if x.Policy == "" {
		x.Policy = "one_factor"
	}
	if len(x.Scopes) == 0 {
		x.Scopes = DefaultScopes
	}
	if x.Consent == "" {
		x.Consent = "pre-configured"
	}
	if x.AuthMethod == "" {
		x.AuthMethod = "client_secret_basic"
	}
	if x.Public {
		x.PKCE = true
	}
	if err := x.check(); err != nil {
		return err
	}
	if _, e := c.find(x.ID); e != nil {
		return fmt.Errorf("client %q already exists", x.ID)
	}
	e := &yaml.Node{Kind: yaml.MappingNode}
	set(e, "client_id", str(x.ID))
	set(e, "client_name", str(x.Name))
	if x.Public {
		set(e, "public", boolean(true))
	} else {
		set(e, "client_secret", quoted(SecretRef(mount, x.ID)))
		set(e, "public", boolean(false))
	}
	set(e, "authorization_policy", str(x.Policy))
	writeConsent(e, x.Consent)
	writePKCE(e, x.PKCE)
	set(e, "redirect_uris", list(x.Redirects))
	set(e, "scopes", flowList(x.Scopes))
	set(e, "response_types", flowList([]string{"code"}))
	set(e, "grant_types", flowList([]string{"authorization_code"}))
	set(e, "access_token_signed_response_alg", str("none"))
	set(e, "userinfo_signed_response_alg", str("none"))
	if x.Public {
		set(e, "token_endpoint_auth_method", str("none"))
	} else {
		set(e, "token_endpoint_auth_method", str(x.AuthMethod))
	}
	s := c.seq()
	s.Content = append(s.Content, e)
	return nil
}

func writeConsent(e *yaml.Node, mode string) {
	set(e, "consent_mode", str(mode))
	if mode == "pre-configured" {
		set(e, "pre_configured_consent_duration", str("1y"))
	} else {
		del(e, "pre_configured_consent_duration")
	}
}

func writePKCE(e *yaml.Node, on bool) {
	set(e, "require_pkce", boolean(on))
	if on {
		set(e, "pkce_challenge_method", str("S256"))
	} else {
		del(e, "pkce_challenge_method")
	}
}

// ClientChange is a partial update; nil fields stay as they are.
type ClientChange struct {
	Name, Policy, Consent, AuthMethod *string
	Redirects, Scopes                 *[]string
	PKCE                              *bool
}

func (c *Clients) Set(id string, ch ClientChange) error {
	_, e := c.find(id)
	if e == nil {
		return fmt.Errorf("no client %q", id)
	}
	x, _ := c.Get(id)
	if ch.Redirects != nil {
		x.Redirects = *ch.Redirects
	}
	if ch.Consent != nil {
		x.Consent = *ch.Consent
	}
	if ch.AuthMethod != nil {
		if x.Public {
			return fmt.Errorf("client %q is public: it has no token endpoint secret", id)
		}
		x.AuthMethod = *ch.AuthMethod
	}
	if ch.PKCE != nil && !*ch.PKCE && x.Public {
		return fmt.Errorf("client %q is public: PKCE stays on", id)
	}
	if err := x.check(); err != nil {
		return err
	}
	if ch.Name != nil {
		set(e, "client_name", str(*ch.Name))
	}
	if ch.Policy != nil {
		set(e, "authorization_policy", str(*ch.Policy))
	}
	if ch.Consent != nil {
		writeConsent(e, *ch.Consent)
	}
	if ch.AuthMethod != nil {
		set(e, "token_endpoint_auth_method", str(*ch.AuthMethod))
	}
	if ch.PKCE != nil {
		writePKCE(e, *ch.PKCE)
	}
	if ch.Redirects != nil {
		set(e, "redirect_uris", list(*ch.Redirects))
	}
	if ch.Scopes != nil {
		set(e, "scopes", flowList(*ch.Scopes))
	}
	return nil
}

// Remove drops a client. Authelia refuses to start without any, so the last
// one cannot go.
func (c *Clients) Remove(id string) error {
	i, e := c.find(id)
	if e == nil {
		return fmt.Errorf("no client %q", id)
	}
	s := c.seq()
	if len(s.Content) == 1 {
		return fmt.Errorf("%q is the only client and Authelia needs at least one; add its replacement first", id)
	}
	s.Content = append(s.Content[:i], s.Content[i+1:]...)
	return nil
}

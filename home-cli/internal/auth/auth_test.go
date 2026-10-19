package auth

import (
	"strings"
	"testing"
)

const usersYAML = `# first admin, seeded by authelia-secrets.sh
users:
  ab18:
    displayname: 'Ashish'
    password: '$argon2id$v=19$m=65536,t=3,p=4$abc$def'
    email: 'me@example.com'
    groups: [admins]
`

func TestUsersRoundTrip(t *testing.T) {
	u, err := ParseUsers([]byte(usersYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Add(User{Name: "alice", Email: "a@x.org", Groups: []string{"family"}}, "$argon2id$x"); err != nil {
		t.Fatal(err)
	}
	if err := u.Add(User{Name: "alice", Email: "b@x.org"}, "h"); err == nil {
		t.Fatal("duplicate user accepted")
	}
	if err := u.Add(User{Name: "Bob!", Email: "b@x.org"}, "h"); err == nil {
		t.Fatal("bad name accepted")
	}
	yes, mail := true, "new@x.org"
	if err := u.Set("ab18", Change{Disabled: &yes, Email: &mail}); err != nil {
		t.Fatal(err)
	}
	out := string(u.Bytes())
	for _, want := range []string{"# first admin", "password: '$argon2id$x'", "displayname: 'alice'", "groups: [family]", "disabled: true", "email: 'new@x.org'"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	u2, err := ParseUsers([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	l := u2.List()
	if len(l) != 2 || l[0].Name != "ab18" || !l[0].Disabled || l[1].Groups[0] != "family" {
		t.Fatalf("list after round trip: %+v", l)
	}
	no := false
	u2.Set("ab18", Change{Disabled: &no})
	if strings.Contains(string(u2.Bytes()), "disabled") {
		t.Error("enable kept disabled:")
	}
	if err := u2.Remove("alice"); err != nil || len(u2.List()) != 1 {
		t.Fatalf("remove: %v %+v", err, u2.List())
	}
	if err := u2.Remove("alice"); err == nil {
		t.Error("removing a missing user succeeded")
	}
}

func TestClients(t *testing.T) {
	c, err := ParseClients(nil)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Add(Client{ID: "immich", Name: "Immich", PKCE: true, Redirects: []string{"https://photos.example.com/auth/login", "app.immich:///oauth-callback"}}, "/secrets")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Add(Client{ID: "spa", Public: true, Redirects: []string{"http://localhost:3000/cb"}}, "/secrets/"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Client{
		{ID: "immich", Redirects: []string{"https://x.example.com"}},
		{ID: "x", Redirects: []string{"http://x.example.com/cb"}},
		{ID: "x"},
		{ID: "x", Redirects: []string{"https://x.example.com"}, Consent: "sometimes"},
	} {
		if err := c.Add(bad, "/secrets"); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	out := string(c.Bytes())
	for _, want := range []string{
		"# OpenID Connect clients",
		`client_secret: '{{ secret "/secrets/immich.digest" }}'`,
		"pkce_challenge_method: S256",
		"consent_mode: pre-configured",
		"pre_configured_consent_duration: 1y",
		"scopes: [openid, profile, email]",
		"token_endpoint_auth_method: client_secret_basic",
		"token_endpoint_auth_method: none",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "spa.digest") {
		t.Error("public client got a secret")
	}

	c2, err := ParseClients([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	rs, explicit, off := []string{"https://photos.example.com/auth/login"}, "explicit", false
	if err := c2.Set("immich", ClientChange{Redirects: &rs, Consent: &explicit, PKCE: &off}); err != nil {
		t.Fatal(err)
	}
	if err := c2.Set("spa", ClientChange{PKCE: &off}); err == nil {
		t.Error("turned PKCE off on a public client")
	}
	x, _ := c2.Get("immich")
	if len(x.Redirects) != 1 || x.Consent != "explicit" || x.PKCE {
		t.Errorf("after set: %+v", x)
	}
	if err := c2.Remove("spa"); err != nil {
		t.Fatal(err)
	}
	if s := string(c2.Bytes()); strings.Contains(s, "pkce_challenge_method") || strings.Contains(s, "pre_configured_consent_duration") {
		t.Errorf("stale keys after set:\n%s", s)
	}
	if err := c2.Remove("immich"); err == nil {
		t.Error("removed the last client")
	}
}

const otc = `Date: 2026-10-19 01:11:20.12345 +0000 UTC m=+1000.1
Recipient: {Ashish Bhatiya me@example.com}
Subject: Confirm your identity
A ONE-TIME CODE HAS BEEN GENERATED TO COMPLETE A REQUESTED ACTION

Hi Ashish Bhatiya,

The following one-time code should only be used in the prompt displayed in your browser.

----------------------------------------

AB12CD34

----------------------------------------

If you did not initiate the process your credentials may have been compromised.

To revoke the code, click the link below:

https://id.example.com/revoke/one-time-code?id=x

----------------------------------------

Powered by Authelia https://www.authelia.com
`

func TestParseNotification(t *testing.T) {
	n, ok := ParseNotification(otc)
	if !ok || n.Code != "AB12CD34" || n.Subject != "Confirm your identity" || n.Recipient != "Ashish Bhatiya me@example.com" {
		t.Fatalf("%+v", n)
	}
	if n.Date.Year() != 2026 || n.Date.Hour() != 1 || n.Date.Minute() != 11 {
		t.Errorf("date %v", n.Date)
	}
	ev := strings.Replace(strings.Replace(otc, "A ONE-TIME CODE HAS BEEN GENERATED", "AN EVENT", 1), "Confirm your identity", "Second Factor Method Added", 1)
	if n, ok := ParseNotification(ev); !ok || n.Code != "" {
		t.Errorf("event got a code: %+v", n)
	}
	if _, ok := ParseNotification("garbage\nmore"); ok {
		t.Error("parsed garbage")
	}
}

func TestParsePasskeys(t *testing.T) {
	out := `WebAuthn Credentials:

ID    RPID          KID                 Description      Username
1     id.ab18.in    AbCd123_-xyz        macbook-air      ab18
2     id.ab18.in    ZZZ                 my old phone     alice
`
	ps := ParsePasskeys(out)
	if len(ps) != 2 || ps[1].Description != "my old phone" || ps[1].Username != "alice" || ps[0].ID != "1" {
		t.Fatalf("%+v", ps)
	}
}

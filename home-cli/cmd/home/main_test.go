package main

import (
	"testing"

	"github.com/ashishbhatiya18/home/internal/homecfg"
)

func TestParseLogin(t *testing.T) {
	cases := map[string][2]string{
		"me:ghp_abc":                 {"me", "ghp_abc"},
		"757365720a746f6b656e313233": {"user", "token123"}, // v0.2.0 entry as the Keychain returns it
	}
	for in, want := range cases {
		u, p, ok := parseLogin(in)
		if !ok || u != want[0] || p != want[1] {
			t.Errorf("parseLogin(%q) = %q %q %v", in, u, p, ok)
		}
	}
	if _, _, ok := parseLogin("nocolon"); ok {
		t.Error("malformed login accepted")
	}
}

func TestNodePath(t *testing.T) {
	n := &homecfg.Node{StacksDir: "/home/dietpi/localstack/stacks"}
	for in, want := range map[string]string{
		"secrets/app.env":       "/home/dietpi/localstack/secrets/app.env",
		"stacks/x/compose.yaml": "/home/dietpi/localstack/stacks/x/compose.yaml",
		"/etc/hosts":            "/etc/hosts",
		"/etc/../etc/hosts":     "/etc/hosts",
	} {
		if got, err := nodePath(n, in); err != nil || got != want {
			t.Errorf("nodePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "secrets/", "~/x"} {
		if _, err := nodePath(n, in); err == nil {
			t.Errorf("nodePath(%q): expected an error", in)
		}
	}
}

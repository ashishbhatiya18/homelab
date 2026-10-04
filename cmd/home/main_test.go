package main

import "testing"

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

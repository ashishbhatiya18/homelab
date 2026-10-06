package service

import "testing"

func TestLoaded(t *testing.T) {
	cases := map[string]bool{
		// brew pretty-prints its JSON; this is what broke v0.1.0.
		"[\n  {\n    \"name\": \"hbr\",\n    \"running\": false,\n    \"loaded\": true\n  }\n]": true,
		`[{"name":"hbr","loaded":false}]`: false,
		`[]`:                              false,
		`not json`:                        false,
	}
	for in, want := range cases {
		if got := loaded([]byte(in)); got != want {
			t.Errorf("loaded(%q) = %v, want %v", in, got, want)
		}
	}
}

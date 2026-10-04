package registry

import "testing"

func TestParse(t *testing.T) {
	cases := map[string]Ref{
		"nginx":                            {"registry-1.docker.io", "library/nginx", "latest"},
		"postgres:14":                      {"registry-1.docker.io", "library/postgres", "14"},
		"nickfedor/watchtower":             {"registry-1.docker.io", "nickfedor/watchtower", "latest"},
		"ghcr.io/org/app:rewrite":          {"ghcr.io", "org/app", "rewrite"},
		"localhost:5000/app:1":             {"localhost:5000", "app", "1"},
		"docker.io/library/redis:7-alpine": {"registry-1.docker.io", "library/redis", "7-alpine"},
	}
	for in, want := range cases {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := Parse("nginx@sha256:abc"); err == nil {
		t.Error("digest-pinned refs should be rejected")
	}
}

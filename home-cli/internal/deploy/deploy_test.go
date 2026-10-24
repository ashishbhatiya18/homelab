package deploy

import (
	"errors"
	"strings"
	"testing"

	"github.com/ashishbhatiya18/home/internal/homecfg"
)

func TestPathsOf(t *testing.T) {
	p := pathsOf(homecfg.Node{StacksDir: "/home/dietpi/localstack/stacks/"})
	if p.live != "/home/dietpi/localstack/stacks" || p.releases != "/home/dietpi/localstack/releases" {
		t.Fatalf("got %+v", p)
	}
	if d := p.content("sha256:abc"); d != "/home/dietpi/localstack/releases/abc/stacks" {
		t.Fatalf("content = %s", d)
	}
}

func TestRepoOf(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/me/node-ab":        "ghcr.io/me/node-ab",
		"ghcr.io/me/node-ab:latest": "ghcr.io/me/node-ab",
		"localhost:5000/node-ab":    "localhost:5000/node-ab",
		"localhost:5000/node-ab:v1": "localhost:5000/node-ab",
	} {
		if got := repoOf(in); got != want {
			t.Errorf("repoOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRelease(t *testing.T) {
	r := parseRelease("revision=6b061a50396e8737\ndigest=sha256:1c6f\nimage=ghcr.io/me/node-ab\nprevious=sha256:99\ndeployed_at=2026-10-07T00:30:00+05:30\n")
	if r.Revision != "6b061a50396e8737" || r.Digest != "sha256:1c6f" || r.Previous != "sha256:99" || r.Short() != "6b061a50396e" {
		t.Fatalf("got %+v", r)
	}
	if (Release{}).Short() != "none" {
		t.Fatal("empty release should read as none")
	}
}

func TestMissingSecrets(t *testing.T) {
	missing := []string{"network /s/network/cf", "vault /s/vault.env", "idle /s/idle"}
	got, mig := missingFor([]string{"network", "vault"}, missing, map[string]bool{"network": true, "idle": true})
	if len(got) != 2 || got[0] != "network /s/network/cf" || got[1] != "vault /s/vault.env" {
		t.Fatalf("missing = %v (idle is not started, so it doesn't count)", got)
	}
	if len(mig) != 1 || !mig["network"] {
		t.Fatalf("migrations = %v", mig)
	}
	p := &Plan{Node: homecfg.Node{Name: "ab"}, MissingSecrets: got, Migrations: mig}
	err := p.MissingError()
	if !errors.Is(err, ErrMissingSecrets) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"network: /s/network/cf", "vault: /s/vault.env", "home secret migrate ab/network", "create the others"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q not in %q", want, err)
		}
	}
	if (&Plan{}).MissingError() != nil {
		t.Error("no missing secrets should be no error")
	}
}

package deploy

import (
	"testing"

	"github.com/ashishbhatiya18/home/internal/homecfg"
)

func TestPathsOf(t *testing.T) {
	p := pathsOf(homecfg.Node{StacksDir: "/home/dietpi/localstack/nodes/ab/"})
	if p.live != "/home/dietpi/localstack/nodes/ab" || p.releases != "/home/dietpi/localstack/releases" || p.name != "ab" {
		t.Fatalf("got %+v", p)
	}
	if d := p.releaseDir("sha256:abc"); d != "/home/dietpi/localstack/releases/abc" {
		t.Fatalf("releaseDir = %s", d)
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

package remote

import (
	"fmt"
	"net"
	"testing"
)

func TestParseSSHG(t *testing.T) {
	out := "user dietpi\nhostname 10.10.10.11\nport 2222\nbatchmode no\n"
	if got := parseSSHG(out); got != "10.10.10.11:2222" {
		t.Fatalf("got %q", got)
	}
	if got := parseSSHG("user x\n"); got != "" {
		t.Fatalf("no hostname: got %q", got)
	}
}

// closedPort returns a local port nothing listens on.
func closedPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func TestTargetFallback(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	up := fmt.Sprintf("ssh://u@127.0.0.1:%d", l.Addr().(*net.TCPAddr).Port)
	down := fmt.Sprintf("ssh://u@127.0.0.1:%d", closedPort(t))

	cases := []struct {
		name, primary, fallback, want string
	}{
		{"primary answers", up, down, up},
		{"primary down", down, up, up},
		{"both down keeps primary", down, down + "0", down},
		{"no fallback", down, "", down},
	}
	for _, c := range cases {
		r := &Runner{Hosts: map[string]string{"n": c.primary}, Fallback: map[string]string{"n": c.fallback}}
		if got := r.Target("n"); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// The choice sticks for the runner's lifetime.
	r := &Runner{Hosts: map[string]string{"n": down}, Fallback: map[string]string{"n": up}}
	r.Target("n")
	l.Close()
	if got := r.Target("n"); got != up {
		t.Errorf("cached: got %q, want %q", got, up)
	}
}

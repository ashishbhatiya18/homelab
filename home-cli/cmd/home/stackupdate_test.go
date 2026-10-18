package main

import (
	"testing"

	"github.com/ashishbhatiya18/home/internal/node"
	"github.com/ashishbhatiya18/home/internal/stack"
)

func TestRunsTailscale(t *testing.T) {
	cases := []struct {
		c    node.Container
		want bool
	}{
		{node.Container{Name: "tailscale", Image: "sha256:abc"}, true}, // tag moved: bare ID
		{node.Container{Name: "ts", Image: "tailscale/tailscale:latest"}, true},
		{node.Container{Name: "traefik", Image: "traefik:latest"}, false},
	}
	for _, c := range cases {
		s := stack.Stack{Containers: []node.Container{{Name: "pihole", Image: "pihole/pihole"}, c.c}}
		if got := runsTailscale(s); got != c.want {
			t.Errorf("%+v: got %v", c.c, got)
		}
	}
}

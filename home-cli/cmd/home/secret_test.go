package main

import (
	"reflect"
	"testing"

	"github.com/ashishbhatiya18/home/internal/secretfiles"
)

func TestNotCreated(t *testing.T) {
	moves := []secretfiles.Move{{From: "cd-monitor.env", To: "monitor.env"}, {From: "authelia", To: "network/authelia"}, {From: "x", To: "done"}}
	states := map[int]string{1: "pending", 2: "pending", 3: "done"}
	missing := []string{"monitor /s/monitor.env", "network /s/network/authelia/jwt", "network /s/network/other", "x /s/done"}
	got := notCreated(missing, "/s", moves, states)
	want := []string{"network /s/network/other", "x /s/done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

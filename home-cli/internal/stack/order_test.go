package stack

import (
	"reflect"
	"testing"
)

func TestOrderNames(t *testing.T) {
	got, first, _ := orderNames([]string{"network", "data"}, []string{"immich", "data", "bentopdf", "network"})
	if want := []string{"network", "data", "bentopdf", "immich"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !first["network"] || !first["data"] || first["immich"] {
		t.Fatalf("first = %v", first)
	}
	got, first, _ = orderNames([]string{"network", "data"}, []string{"vaultwarden", "data"})
	if want := []string{"data", "vaultwarden"}; !reflect.DeepEqual(got, want) || len(first) != 1 {
		t.Fatalf("got %v %v", got, first)
	}
	got, first, _ = orderNames(nil, []string{"b", "a"})
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) || len(first) != 0 {
		t.Fatalf("no ORDER: got %v %v", got, first)
	}
}

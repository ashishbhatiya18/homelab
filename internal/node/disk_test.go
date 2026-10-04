package node

import "testing"

func TestParseSize(t *testing.T) {
	cases := map[string]int64{"1.5GB": 3 << 29, "512MB": 512 << 20, "120kB": 120 << 10, "0B": 0, "": 0, "88M": 88 << 20}
	for in, want := range cases {
		if got := ParseSize(in); got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
	if !olderThan("2020-01-02 03:04:05 +0000 UTC", 30) || olderThan("2999-01-01 00:00:00 +0000 UTC", 30) {
		t.Error("olderThan wrong")
	}
}

func TestLeaves(t *testing.T) {
	dirs := []Dir{{"/home", 300}, {"/home/dietpi", 295}, {"/home/dietpi/data", 290}, {"/var", 20}, {"/var/lib", 18}, {"/usr", 5}}
	got := leaves(dirs, 10)
	if len(got) != 2 || got[0].Path != "/home/dietpi/data" || got[1].Path != "/var/lib" {
		t.Fatalf("leaves = %+v", got)
	}
}

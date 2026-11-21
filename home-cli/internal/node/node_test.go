package node

import "testing"

func TestParseContainers(t *testing.T) {
	out := `traefik|traefik:v3.0|running|Up 2 hours|network
ntfy|binwiederhier/ntfy:latest|running|Up 5 days (healthy)|ntfy
pihole|pihole/pihole:latest|restarting|Restarting (1) 5 seconds ago|network
invalid_line
`
	ctrs := parseContainers(out)
	if len(ctrs) != 3 {
		t.Fatalf("len(ctrs) = %d, want 3", len(ctrs))
	}
	// Sorted by name: ntfy, pihole, traefik
	if ctrs[0].Name != "ntfy" || ctrs[0].Project != "ntfy" {
		t.Errorf("ctrs[0] = %+v", ctrs[0])
	}
	if ctrs[1].Name != "pihole" || !ctrs[1].Healthy() == false {
		t.Errorf("ctrs[1] = %+v", ctrs[1])
	}
	if ctrs[2].Name != "traefik" || ctrs[2].State != "running" {
		t.Errorf("ctrs[2] = %+v", ctrs[2])
	}
}

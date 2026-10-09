package stack

import "testing"

func TestParseRunning(t *testing.T) {
	out := `immich-server|ghcr.io/immich-app/immich-server:release|sha256:117a|["ghcr.io/immich-app/immich-server@sha256:old"]
built|local/app:dev|sha256:ccc|[]
garbage line
`
	refs, local := parseRunning(out)
	if refs["immich-server"] != "ghcr.io/immich-app/immich-server:release" {
		t.Errorf("ref = %q, want the configured image, not the ID", refs["immich-server"])
	}
	if !local["immich-server"]["sha256:old"] || len(local["immich-server"]) != 1 {
		t.Errorf("digests = %v", local["immich-server"])
	}
	if len(local["built"]) != 0 || len(refs) != 2 {
		t.Errorf("refs = %v, local = %v", refs, local)
	}
}

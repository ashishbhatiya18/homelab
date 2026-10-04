package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectsUnknownStopHost(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte(`
destination: /tmp/x
hosts: {a: {ssh: u@h}}
apps:
  - name: app
    sources: [{type: postgres, name: db, host: a, container: pg, database: d}]
    restore: {stop: [{host: nope, containers: [x]}]}
`), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for unknown host")
	}
}

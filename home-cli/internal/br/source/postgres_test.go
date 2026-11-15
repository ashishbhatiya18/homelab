package source

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFilterSettingsForOlderServers(t *testing.T) {
	in := "SET statement_timeout = 0;\nSET transaction_timeout = 0;\nCREATE TABLE x();\n"
	var out bytes.Buffer
	if err := filterSettings(strings.NewReader(in), &out, 14); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "transaction_timeout") {
		t.Fatal("transaction_timeout must be dropped for Postgres 14")
	}
	if !strings.Contains(out.String(), "statement_timeout") || !strings.Contains(out.String(), "CREATE TABLE") {
		t.Fatal("other statements must be kept")
	}
	out.Reset()
	filterSettings(strings.NewReader(in), &out, 17)
	if !strings.Contains(out.String(), "transaction_timeout") {
		t.Fatal("transaction_timeout must be kept for Postgres 17+")
	}
}

func TestDrillImage(t *testing.T) {
	var nodes []yaml.Node
	src := `
- {type: postgres-docker, name: plain, host: h, container: pg, database: db}
- {type: postgres-docker, name: immich, host: h, container: pg, database: photos, drill_image: "ghcr.io/immich-app/postgres:18"}
`
	if err := yaml.Unmarshal([]byte(src), &nodes); err != nil {
		t.Fatal(err)
	}
	list, err := Build("app", nodes, map[string]string{"h": "h"})
	if err != nil {
		t.Fatal(err)
	}
	img := func(s Source) string { return s.(interface{ Image(int) string }).Image(18) }
	if got := img(list[0]); got != "postgres:18" {
		t.Fatalf("default drill image: got %q", got)
	}
	if got := img(list[1]); got != "ghcr.io/immich-app/postgres:18" {
		t.Fatalf("drill_image: got %q", got)
	}
}

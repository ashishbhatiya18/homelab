package source

import (
	"bytes"
	"strings"
	"testing"
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

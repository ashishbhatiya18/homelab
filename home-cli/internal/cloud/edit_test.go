package cloud

import (
	"os"
	"strings"
	"testing"
)

func TestRuleEditsKeepComments(t *testing.T) {
	dir := t.TempDir()
	f := Compact("ab", liveConfig())
	if _, err := SaveTunnel(dir, f); err != nil {
		t.Fatal(err)
	}
	p := TunnelPath(dir, "ab")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "  - code.ab18.in\n", "  - code.ab18.in # the editor\n", 1)), 0o644)

	if old, err := SetRule(dir, "ab", Rule{Hostname: "pad.ab18.in", Short: true}); err != nil || old != nil {
		t.Fatalf("add: %v %v", old, err)
	}
	if old, err := SetRule(dir, "ab", Rule{Hostname: "code.ab18.in", Service: "http://code:8080"}); err != nil || old == nil || !old.Short {
		t.Fatalf("replace: %+v %v", old, err)
	}
	if _, err := RemoveRule(dir, "ab", "vault.ab18.in", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveRule(dir, "ab", "nope.ab18.in", ""); err != ErrNoRule {
		t.Fatalf("remove missing: %v", err)
	}

	b, _ = os.ReadFile(p)
	text := string(b)
	if !strings.Contains(text, "# the editor") || !strings.Contains(text, "# Cloudflare Tunnel ab") {
		t.Errorf("comments lost:\n%s", text)
	}
	fs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := fs.Tunnel("ab")
	var keys []string
	for _, r := range got.Ingress {
		k := r.Hostname
		if r.Short {
			k += "(short)"
		}
		if k == "" {
			k = r.Service
		}
		keys = append(keys, k)
	}
	if strings.Join(keys, " ") != "auth.ab18.in(short) code.ab18.in ntfy.ab18.in pad.ab18.in(short) http_status:404" {
		t.Fatalf("ingress = %v\n%s", keys, text)
	}
	if _, err := got.Expand(); err != nil {
		t.Fatal(err)
	}
}

func TestShortenResolved(t *testing.T) {
	f := Compact("ab", liveConfig())
	full := f.Resolved(Rule{Hostname: "x.ab18.in", Short: true})
	if full.Short || full.Service != "https://traefik:443" || full.OriginRequest["httpHostHeader"] != "x.ab18.in" {
		t.Fatalf("Resolved = %+v", full)
	}
	if !f.Shorten(full).Short {
		t.Fatal("defaults-equal rule not shortened")
	}
	full.Service = "http://x:80"
	if f.Shorten(full).Short {
		t.Fatal("custom rule shortened")
	}
}

func TestRecordEdits(t *testing.T) {
	dir := t.TempDir()
	if err := AddRecord(dir, "x.in", Record{Name: "@", Type: "TXT", Content: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := AddRecord(dir, "x.in", Record{Name: "www", Type: "CNAME", Content: "x.in", Proxied: true}); err != nil {
		t.Fatal(err)
	}
	rm, err := RemoveRecords(dir, "x.in", func(r Record) bool { return r.Type == "TXT" })
	if err != nil || len(rm) != 1 {
		t.Fatalf("removed %v %v", rm, err)
	}
	fs, _ := Load(dir)
	if z := fs.Zone("x.in"); z == nil || len(z.Records) != 1 || z.Records[0].Name != "www" {
		t.Fatalf("zone = %+v", z)
	}
	b, _ := os.ReadFile(ZonePath(dir, "x.in"))
	if !strings.Contains(string(b), "- {name: www, type: CNAME, content: x.in, proxied: true}") {
		t.Errorf("record not in flow style:\n%s", b)
	}
}

func TestSetField(t *testing.T) {
	dir := t.TempDir()
	f := Compact("ab", liveConfig())
	f.Node, f.Stack, f.Container = "ab", "network", "cloudflared"
	SaveTunnel(dir, f)
	if err := SetField(dir, "ab", "secret", "/x/tok"); err != nil {
		t.Fatal(err)
	}
	if err := SetField(dir, "ab", "node", "cd"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(TunnelPath(dir, "ab"))
	if !strings.Contains(string(b), "container: cloudflared\nsecret: /x/tok\n") || !strings.Contains(string(b), "node: cd\n") {
		t.Fatalf("file:\n%s", b)
	}
}

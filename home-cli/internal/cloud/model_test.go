package cloud

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashishbhatiya18/home/internal/cloudflare"
)

func traefikRule(h string) cloudflare.IngressRule {
	return cloudflare.IngressRule{Hostname: h, Service: "https://traefik:443",
		OriginRequest: map[string]any{"httpHostHeader": h, "originServerName": h}}
}

func liveConfig() *cloudflare.TunnelConfig {
	return &cloudflare.TunnelConfig{
		OriginRequest: map[string]any{"http2Origin": true, "noTLSVerify": false},
		WarpRouting:   map[string]any{"enabled": false},
		Ingress: []cloudflare.IngressRule{
			traefikRule("auth.ab18.in"),
			traefikRule("code.ab18.in"),
			{Hostname: "ntfy.ab18.in", Service: "http://ntfy:80"},
			traefikRule("vault.ab18.in"),
			{Service: "http_status:404"},
		},
	}
}

func TestCompactAndExpandRoundTrip(t *testing.T) {
	live := liveConfig()
	f := Compact("ab18-localstack", live)
	if f.Defaults == nil || f.Defaults.Service != "https://traefik:443" || f.Defaults.OriginRequest["httpHostHeader"] != HostVar {
		t.Fatalf("defaults = %+v", f.Defaults)
	}
	if f.WarpRouting != nil {
		t.Errorf("default warp-routing kept: %v", f.WarpRouting)
	}
	short := 0
	for _, r := range f.Ingress {
		if r.Short {
			short++
		}
	}
	if short != 3 || f.Ingress[2].Short || f.Ingress[2].Service != "http://ntfy:80" {
		t.Fatalf("ingress = %+v", f.Ingress)
	}

	got, err := f.Expand()
	if err != nil {
		t.Fatal(err)
	}
	if canon(got.Ingress) != canon(live.Ingress) || canon(got.OriginRequest) != canon(live.OriginRequest) {
		t.Fatalf("round trip:\n got %s\nwant %s", canon(got.Ingress), canon(live.Ingress))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := Compact("ab18-localstack", liveConfig())
	f.ID, f.Node, f.Stack, f.Container = "t-1", "ab", "network", "cloudflared"
	p, err := SaveTunnel(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	text := string(b)
	for _, want := range []string{"  - auth.ab18.in\n", "httpHostHeader: '{host}'", "- service: http_status:404", "hostname: ntfy.ab18.in"} {
		if !strings.Contains(text, want) {
			t.Errorf("file lacks %q:\n%s", want, text)
		}
	}

	prio := 10
	z := ZoneFile{Zone: "ab18.in", Records: []Record{
		{Name: "mail", Type: "MX", Content: "mx1.example.com", Priority: &prio},
		{Name: "@", Type: "CNAME", Content: "ab18.pages.dev", Proxied: true},
	}}
	SortRecords(z.Records)
	if _, err := SaveZone(dir, z); err != nil {
		t.Fatal(err)
	}
	zb, _ := os.ReadFile(filepath.Join(dir, "zones", "ab18.in.yaml"))
	if !strings.Contains(string(zb), "- {name: '@', type: CNAME, content: ab18.pages.dev, proxied: true}") {
		t.Errorf("zone file:\n%s", zb)
	}

	fs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	lf := fs.Tunnel("ab18-localstack")
	if lf == nil || lf.Node != "ab" || len(fs.Zones) != 1 || *fs.Zones[0].Records[1].Priority != 10 {
		t.Fatalf("loaded %+v", fs)
	}
	a, _ := lf.Expand()
	e, _ := f.Expand()
	if canon(a) != canon(e) {
		t.Fatalf("after load:\n%s\n%s", canon(a), canon(e))
	}
}

func TestExpandErrors(t *testing.T) {
	cases := map[string]TunnelFile{
		"no defaults":    {Tunnel: "t", Ingress: []Rule{{Hostname: "a.x", Short: true}, {Service: "http_status:404"}}},
		"no catch-all":   {Tunnel: "t", Ingress: []Rule{{Hostname: "a.x", Service: "http://a"}}},
		"duplicate host": {Tunnel: "t", Ingress: []Rule{{Hostname: "a.x", Service: "http://a"}, {Hostname: "a.x", Service: "http://b"}, {Service: "http_status:404"}}},
		"no service":     {Tunnel: "t", Ingress: []Rule{{Hostname: "a.x"}, {Service: "http_status:404"}}},
	}
	for name, f := range cases {
		if _, err := f.Expand(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestNames(t *testing.T) {
	if RelName("ab18.in", "ab18.in") != "@" || RelName("a.b.ab18.in", "ab18.in") != "a.b" {
		t.Fatal("RelName")
	}
	if AbsName("@", "ab18.in") != "ab18.in" || AbsName("www", "ab18.in") != "www.ab18.in" {
		t.Fatal("AbsName")
	}
}

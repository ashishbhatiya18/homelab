package cloud

import (
	"strings"
	"testing"

	"github.com/ashishbhatiya18/home/internal/cloudflare"
)

func TestExport(t *testing.T) {
	cfg := &cloudflare.TunnelConfig{Ingress: []cloudflare.IngressRule{
		traefikRule("auth.ab18.in"), traefikRule("stack.ab18.in"), traefikRule("ntfy.ab18.in"), {Service: "http_status:404"},
	}}
	st := &State{
		Tunnels: []TunnelState{{Tunnel: cloudflare.Tunnel{ID: "t1", Name: "ab18-localstack", ConfigSrc: "cloudflare"}, Config: cfg},
			{Tunnel: cloudflare.Tunnel{ID: "t2", Name: "laptop", ConfigSrc: "local"}}},
		Runners: map[string][]Runner{"t1": {{Node: "ab", Stack: "network", Container: "cloudflared"}}},
		Zones: []ZoneState{{Zone: cloudflare.Zone{ID: "z", Name: "ab18.in"}, Records: []cloudflare.DNSRecord{
			{Name: "auth.ab18.in", Type: "CNAME", Content: "t1" + TunnelSuffix, Proxied: true, TTL: 1},
			{Name: "stack.ab18.in", Type: "CNAME", Content: "t1" + TunnelSuffix, Proxied: true, TTL: 1},
			{Name: "old.ab18.in", Type: "CNAME", Content: "gone" + TunnelSuffix, Proxied: true, TTL: 1},
			{Name: "pad.ab18.in", Type: "CNAME", Content: "t1" + TunnelSuffix, Proxied: true, TTL: 1},
			{Name: "ab18.in", Type: "CNAME", Content: "ab18.pages.dev", Proxied: true, TTL: 1},
			{Name: "ab18.in", Type: "TXT", Content: "v=spf1 -all", TTL: 3600},
			{Name: "_acme-challenge.pihole.ab18.in", Type: "TXT", Content: "x", TTL: 120},
			{Name: "_dmarc.ab18.in", Type: "TXT", Content: "v=DMARC1"},
		}}},
	}
	prev := &Files{Zones: []ZoneFile{{Zone: "ab18.in", Ignore: []string{"_dmarc"}}}}
	fs, notes := Export(st, prev)
	if len(fs.Tunnels) != 2 || fs.Tunnels[0].Node != "ab" || fs.Tunnels[0].Container != "cloudflared" || !fs.Tunnels[1].Local {
		t.Fatalf("tunnels = %+v", fs.Tunnels)
	}
	recs := fs.Zones[0].Records
	var names []string
	for _, r := range recs {
		names = append(names, r.Name+"/"+r.Type)
	}
	// auth and stack are implied by the tunnel file; old and pad are kept and noted.
	if got := strings.Join(names, " "); got != "@/CNAME @/TXT old/CNAME pad/CNAME" {
		t.Fatalf("records = %s", got)
	}
	if ig := fs.Zones[0].Ignore; len(ig) != 1 || ig[0] != "_dmarc" {
		t.Errorf("ignore list not carried over: %v", ig)
	}
	if recs[0].TTL != 0 || recs[1].TTL != 3600 {
		t.Errorf("ttl: %+v", recs[:2])
	}
	all := strings.Join(notes, "\n")
	for _, want := range []string{
		"tunnel laptop: no cloudflared container",
		"tunnel laptop: ingress is in cloudflared's local config",
		"old.ab18.in: CNAME to a tunnel that no longer exists",
		"pad.ab18.in: CNAME to tunnel ab18-localstack, which has no route",
		"ntfy.ab18.in: routed by tunnel ab18-localstack but has no DNS record",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("notes lack %q:\n%s", want, all)
		}
	}
}

func TestProbeProblem(t *testing.T) {
	if cfErrorCode("<title>Error 1033 · Cloudflare Tunnel error</title>") != "1033" || cfErrorCode("Error 12345") != "" {
		t.Fatal("cfErrorCode")
	}
	ok := []Probe{{Status: 200}, {Status: 302, Location: "auth.ab18.in"}, {Status: 401}}
	for _, p := range ok {
		if p.Problem() != "" {
			t.Errorf("%+v: %s", p, p.Problem())
		}
	}
	bad := []Probe{{Status: 530, CFError: "1033"}, {Status: 502}}
	for _, p := range bad {
		if p.Problem() == "" {
			t.Errorf("%+v: no problem reported", p)
		}
	}
	if p := (Probe{Status: 404}); p.Problem() != "" || p.Warning() == "" {
		t.Error("404 should warn, not fail")
	}
	if (Probe{Status: 302, Location: "auth.ab18.in"}).Result() != "302 → auth.ab18.in" {
		t.Error("Result")
	}
}

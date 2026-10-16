package main

import (
	"strings"
	"testing"

	"github.com/ashishbhatiya18/home/internal/cloud"
	"github.com/ashishbhatiya18/home/internal/cloudflare"
)

func TestRouteRows(t *testing.T) {
	rule := func(h string) cloudflare.IngressRule {
		return cloudflare.IngressRule{Hostname: h, Service: "https://traefik:443",
			OriginRequest: map[string]any{"httpHostHeader": h, "originServerName": h}}
	}
	cname := func(h, id string) cloudflare.DNSRecord {
		return cloudflare.DNSRecord{Name: h, Type: "CNAME", Content: id + cloud.TunnelSuffix, Proxied: true}
	}
	st := &cloud.State{
		Tunnels: []cloud.TunnelState{{Tunnel: cloudflare.Tunnel{ID: "t1", Name: "ab18", Status: "healthy", ConfigSrc: "cloudflare"},
			Config: &cloudflare.TunnelConfig{Ingress: []cloudflare.IngressRule{rule("code.ab18.in"), rule("stack.ab18.in"), rule("ntfy.ab18.in"), {Service: "http_status:404"}}}}},
		Runners: map[string][]cloud.Runner{"t1": {{Node: "ab", Stack: "network"}}},
		Zones: []cloud.ZoneState{{Zone: cloudflare.Zone{Name: "ab18.in"}, Records: []cloudflare.DNSRecord{
			cname("code.ab18.in", "t1"), cname("stack.ab18.in", "t1"), cname("pad.ab18.in", "t1"),
		}}},
		Routers: []cloud.Router{
			{Host: "code.ab18.in", Node: "ab", Stack: "codeserver", Middlewares: []string{"auth"}},
			{Host: "ntfy.ab18.in", Node: "ab", Stack: "ntfy"},
			{Host: "esphome.ab18.in", Node: "ab", Stack: "homeautomation", Middlewares: []string{"auth"}},
		},
	}
	rows, internal := routeRows(st, false)
	if internal != 1 {
		t.Errorf("internal = %d", internal)
	}
	got := map[string]routeRow{}
	for _, r := range rows {
		got[r.host] = r
	}
	if r := got["code.ab18.in"]; len(r.notes) != 0 || r.auth != "yes" || r.traefik != "ab/codeserver" || r.dns != "✓" {
		t.Errorf("code: %+v", r)
	}
	if r := got["stack.ab18.in"]; strings.Join(r.notes, ";") != "no Traefik router" {
		t.Errorf("stack: %+v", r)
	}
	if r := got["ntfy.ab18.in"]; strings.Join(r.notes, ";") != "DNS no record" || r.auth != "no" {
		t.Errorf("ntfy: %+v", r)
	}
	if r := got["pad.ab18.in"]; !strings.Contains(strings.Join(r.notes, ";"), "no ingress rule") {
		t.Errorf("pad: %+v", r)
	}
	if _, ok := got["esphome.ab18.in"]; ok {
		t.Error("internal host listed without --all")
	}
	rows, _ = routeRows(st, true)
	if len(rows) != 5 {
		t.Errorf("--all rows = %d", len(rows))
	}
}

func TestOrigin(t *testing.T) {
	r := cloudflare.IngressRule{Hostname: "a.x", OriginRequest: map[string]any{"httpHostHeader": "a.x", "originServerName": "a.x", "noTLSVerify": true}}
	if got := origin(r); got != "host+sni noTLSVerify=true" {
		t.Fatalf("origin = %q", got)
	}
}

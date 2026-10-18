package cloud

import (
	"strings"
	"testing"

	"github.com/ashishbhatiya18/home/internal/cloudflare"
)

func planState() *State {
	return &State{
		Tunnels: []TunnelState{
			{Tunnel: cloudflare.Tunnel{ID: "t1", Name: "ab", ConfigSrc: "cloudflare"}, Config: &cloudflare.TunnelConfig{
				Ingress: []cloudflare.IngressRule{traefikRule("code.ab18.in"), traefikRule("stack.ab18.in"), {Service: "http_status:404"}}}},
			{Tunnel: cloudflare.Tunnel{ID: "t2", Name: "cd", ConfigSrc: "cloudflare"}, Config: &cloudflare.TunnelConfig{
				Ingress: []cloudflare.IngressRule{traefikRule("api.citrusdental.in"), {Service: "http_status:404"}}}},
		},
		Zones: []ZoneState{
			{Zone: cloudflare.Zone{ID: "z1", Name: "ab18.in"}, Records: []cloudflare.DNSRecord{
				{ID: "r1", Name: "code.ab18.in", Type: "CNAME", Content: "t1" + TunnelSuffix, Proxied: true, TTL: 1},
				{ID: "r2", Name: "stack.ab18.in", Type: "CNAME", Content: "t1" + TunnelSuffix, Proxied: true, TTL: 1},
				{ID: "r3", Name: "ab18.in", Type: "TXT", Content: `"v=spf1 -all"`, TTL: 1},
				{ID: "r4", Name: "_acme-challenge.x.ab18.in", Type: "TXT", Content: "abc", TTL: 120},
				{ID: "r5", Name: "cf2024-1._domainkey.ab18.in", Type: "TXT", Content: "k", TTL: 1, Meta: map[string]any{"read_only": true}},
				{ID: "r6", Name: "www.ab18.in", Type: "A", Content: "1.2.3.4", Proxied: true, TTL: 1},
			}},
			{Zone: cloudflare.Zone{ID: "z2", Name: "citrusdental.in"}, Records: []cloudflare.DNSRecord{
				{ID: "c1", Name: "api.citrusdental.in", Type: "CNAME", Content: "t2" + TunnelSuffix, Proxied: true, TTL: 1},
				{ID: "c2", Name: "citrusdental.in", Type: "CNAME", Content: "site.pages.dev", Proxied: true, TTL: 1},
			}},
		},
	}
}

func planFiles() *Files {
	st := planState()
	fs, _ := Export(st, nil)
	return &fs
}

func TestPlanNoChanges(t *testing.T) {
	st := planState()
	d, _, err := FromFiles(planFiles(), st)
	if err != nil {
		t.Fatal(err)
	}
	if cs := Plan(d, st); len(cs) != 0 {
		for _, c := range cs {
			t.Log(c)
		}
		t.Fatalf("export → plan should be empty, got %d changes", len(cs))
	}
}

func TestPlanRouteMoveAndDNS(t *testing.T) {
	st := planState()
	fs := planFiles()
	ab, cd := fs.Tunnel("ab"), fs.Tunnel("cd")
	// Move code.ab18.in to tunnel cd, drop stack, add ntfy on ab.
	ab.Ingress = []Rule{{Hostname: "ntfy.ab18.in", Service: "http://ntfy:80"}, {Service: "http_status:404"}}
	cd.Ingress = append([]Rule{{Hostname: "code.ab18.in", Service: "https://traefik:443"}}, cd.Ingress...)
	z := fs.Zone("ab18.in")
	z.Records = append(z.Records, Record{Name: "www", Type: "CNAME", Content: "ab18.pages.dev", Proxied: true}) // A → CNAME
	for i, r := range z.Records {
		if r.Name == "www" && r.Type == "A" {
			z.Records = append(z.Records[:i], z.Records[i+1:]...)
			break
		}
	}

	d, _, err := FromFiles(fs, st)
	if err != nil {
		t.Fatal(err)
	}
	cs := Plan(d, st)
	var got []string
	for _, c := range cs {
		got = append(got, firstLine(c.String()))
	}
	want := []string{
		"tunnel ab: ingress", // interim: ntfy added, code + stack kept
		"tunnel cd: ingress",
		"dns ab18.in: - www A 1.2.3.4 (proxied)", // must precede the CNAME
		"dns ab18.in: ~ code CNAME t1.cfargotunnel.com (proxied) → t2.cfargotunnel.com (proxied)",
		"dns ab18.in: + ntfy CNAME t1.cfargotunnel.com (proxied)",
		"dns ab18.in: + www CNAME ab18.pages.dev (proxied)",
		"dns ab18.in: - stack CNAME t1.cfargotunnel.com (proxied)",
		"tunnel ab: ingress", // final
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	interim := cs[0].Config.Ingress
	var hosts []string
	for _, r := range interim {
		hosts = append(hosts, ruleKey(r))
	}
	if strings.Join(hosts, ",") != "ntfy.ab18.in,code.ab18.in,stack.ab18.in,(everything else)" {
		t.Errorf("interim ingress = %v", hosts)
	}
	if n := len(cs[len(cs)-1].Config.Ingress); n != 2 {
		t.Errorf("final ingress has %d rules", n)
	}
	// Ignored and read-only records are never touched.
	for _, c := range cs {
		if c.Old != nil && (c.Old.ID == "r4" || c.Old.ID == "r5") {
			t.Errorf("touched %s", c.Old.Name)
		}
	}
}

func TestPlanZoneWithoutFile(t *testing.T) {
	st := planState()
	fs := planFiles()
	fs.Zones = []ZoneFile{*fs.Zone("ab18.in")} // citrusdental.in has no file
	cd := fs.Tunnel("cd")
	cd.Ingress = []Rule{{Service: "http_status:404"}} // drop api route
	d, _, err := FromFiles(fs, st)
	if err != nil {
		t.Fatal(err)
	}
	cs := Plan(d, st)
	var got []string
	for _, c := range cs {
		got = append(got, firstLine(c.String()))
	}
	// The route's CNAME goes; the Pages apex (unmanaged without a file) stays.
	if strings.Join(got, "\n") != "dns citrusdental.in: - api CNAME t2.cfargotunnel.com (proxied)\ntunnel cd: ingress" {
		t.Fatalf("plan:\n%s", strings.Join(got, "\n"))
	}
}

func TestPlanTXTQuotesAndTTL(t *testing.T) {
	dz := DesiredZone{ID: "z", Name: "x.in", Full: true, Records: []cloudflare.DNSRecord{
		{Name: "x.in", Type: "TXT", Content: "v=spf1 -all"},
		{Name: "a.x.in", Type: "CNAME", Content: "B.example.com.", Proxied: true, TTL: 300},
	}}
	live := []cloudflare.DNSRecord{
		{ID: "1", Name: "x.in", Type: "TXT", Content: `"v=spf1 -all"`, TTL: 1},
		{ID: "2", Name: "a.x.in", Type: "CNAME", Content: "b.example.com", Proxied: true, TTL: 1},
	}
	if cs := dnsChanges(dz, live); len(cs) != 0 {
		t.Fatalf("changes: %v", cs)
	}
}

func TestSnapshotUndo(t *testing.T) {
	st := planState()
	fs := planFiles()
	fs.Tunnel("ab").Ingress = []Rule{{Service: "http_status:404"}}
	d, _, _ := FromFiles(fs, st)
	cs := Plan(d, st)
	snap := TakeSnapshot(st, cs)
	if len(snap.Tunnels) != 1 || len(snap.Zones) != 1 || len(snap.Zones[0].Records) != 6 {
		t.Fatalf("snapshot = %+v", snap)
	}
	// Undo against the unchanged state is a no-op.
	if back := Plan(snap.Desired(func(string) []string { return nil }), st); len(back) != 0 {
		t.Fatalf("undo plan on unchanged state: %v", back)
	}
}

package cloud

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashishbhatiya18/home/internal/cloudflare"
)

func TestParseDiscover(t *testing.T) {
	out := strings.Join([]string{
		"cfd=cloudflared|network|running||4F1E2D3C-0000-4000-8000-000000000001|/home/dietpi/localstack/secrets/tok",
		"cfd=old-cloudflare|citrusdental|exited||", // token unreadable
		`labels=/vaultwarden|vaultwarden|{"traefik.http.routers.vault.rule":"Host(` + "`vault.ab18.in`" + `)","traefik.http.routers.vault.middlewares":"vault-headers, auth","traefik.http.routers.vault-ws.rule":"Host(` + "`vault.ab18.in`" + `) && Path(` + "`/notifications/hub`" + `)"}`,
		"noise",
	}, "\n")
	runners, routers := parseDiscover("ab", out)
	rs := runners["4f1e2d3c-0000-4000-8000-000000000001"]
	if len(rs) != 1 || rs[0].Stack != "network" || rs[0].Container != "cloudflared" || rs[0].State != "running" || rs[0].Secret != "/home/dietpi/localstack/secrets/tok" {
		t.Fatalf("runners = %+v", runners)
	}
	if len(runners[""]) != 1 {
		t.Errorf("container without a readable token dropped: %+v", runners)
	}
	if len(routers) != 2 {
		t.Fatalf("routers = %+v", routers)
	}
	for _, r := range routers {
		if r.Host != "vault.ab18.in" || r.Node != "ab" || r.Container != "vaultwarden" {
			t.Errorf("router %+v", r)
		}
		if r.Name == "vault" && !r.Protected() {
			t.Errorf("vault router not protected: %v", r.Middlewares)
		}
	}
}

// The token decoding part of the node script, run against a real shell.
func TestDiscoverScriptTunnelID(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	fn := strings.SplitN(discoverScript, "\n", 3)[1]
	if !strings.HasPrefix(fn, "tunnel_id()") {
		t.Fatalf("script layout changed: %q", fn)
	}
	tok := base64.StdEncoding.EncodeToString([]byte(`{"a":"acc","t":"4f1e2d3c-0000-4000-8000-000000000001","s":"c2VjcmV0"}`))
	p := filepath.Join(t.TempDir(), "s.sh")
	os.WriteFile(p, []byte(fn+"\nprintf '%s\\n' \"$1\" | tunnel_id\n"), 0o700)
	out, err := exec.Command("bash", p, tok).Output()
	if err != nil || strings.TrimSpace(string(out)) != "4f1e2d3c-0000-4000-8000-000000000001" {
		t.Fatalf("tunnel_id = %q, %v", out, err)
	}
}

func TestStateLookups(t *testing.T) {
	st := &State{Zones: []ZoneState{
		{Zone: cloudflare.Zone{Name: "ab18.in"}, Records: []cloudflare.DNSRecord{
			{Name: "auth.ab18.in", Type: "CNAME", Content: "t1" + TunnelSuffix, Proxied: true},
			{Name: "ab18.in", Type: "CNAME", Content: "ab18.pages.dev"},
		}},
		{Zone: cloudflare.Zone{Name: "sub.ab18.in"}},
	}}
	if z := st.ZoneFor("x.sub.ab18.in"); z == nil || z.Name != "sub.ab18.in" {
		t.Fatalf("ZoneFor longest match = %v", z)
	}
	recs := st.Records("auth.ab18.in")
	if len(recs) != 1 {
		t.Fatalf("records = %v", recs)
	}
	if id, ok := TunnelOfRecord(recs[0]); !ok || id != "t1" {
		t.Fatalf("TunnelOfRecord = %q %v", id, ok)
	}
	if _, ok := TunnelOfRecord(st.Records("ab18.in")[0]); ok {
		t.Fatal("pages CNAME treated as a tunnel")
	}
}

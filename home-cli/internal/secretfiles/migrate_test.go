package secretfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseMoves(t *testing.T) {
	moves, err := ParseMoves([]byte(`
- from: cf_dns_api_token
  to: network/cf_dns_api_token
- from: ab-cloudflared.env
  key: TUNNEL_TOKEN
  to: network/cloudflared_tunnel_token
- from: ab-vaultwarden.env
  keys: [ADMIN_TOKEN, SSO_CLIENT_SECRET]
  to: vaultwarden.env
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Move{
		{From: "cf_dns_api_token", To: "network/cf_dns_api_token"},
		{From: "ab-cloudflared.env", To: "network/cloudflared_tunnel_token", Key: "TUNNEL_TOKEN"},
		{From: "ab-vaultwarden.env", To: "vaultwarden.env", Keys: []string{"ADMIN_TOKEN", "SSO_CLIENT_SECRET"}},
	}
	if !reflect.DeepEqual(moves, want) {
		t.Fatalf("got %+v", moves)
	}
	if s := moves[1].String(); s != "ab-cloudflared.env (TUNNEL_TOKEN) → network/cloudflared_tunnel_token" {
		t.Errorf("String() = %q", s)
	}
}

func TestParseMovesRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":        ``,
		"absolute":     "- {from: /etc/shadow, to: x}",
		"escapes":      "- {from: ../x, to: y}",
		"unclean":      "- {from: a//b, to: y}",
		"no to":        "- {from: a}",
		"same":         "- {from: a, to: a}",
		"key and keys": "- {from: a, to: b, key: K, keys: [L]}",
		"bad key":      "- {from: a, to: b, key: 'K; rm'}",
		"two targets":  "- {from: a, to: c}\n- {from: b, to: c}",
		"unknown":      "- {from: a, to: b, mode: 600}",
	} {
		if _, err := ParseMoves([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseResult(t *testing.T) {
	r := ParseResult("move 1 pending\nmove 2 conflict:network/x exists and differs\ncreated network/x\nremoved old\nkept cf used by dns/compose.yaml\n")
	if r.States[1] != "pending" || r.States[2] != "conflict:network/x exists and differs" {
		t.Errorf("states %v", r.States)
	}
	if !reflect.DeepEqual(r.Created, []string{"network/x"}) || !reflect.DeepEqual(r.Removed, []string{"old"}) ||
		!reflect.DeepEqual(r.Kept, []string{"cf used by dns/compose.yaml"}) {
		t.Errorf("got %+v", r)
	}
}

// TestScriptOnNode runs the node-side scripts in a Debian container, as a
// user with passwordless sudo (like a node's), against a fixture secrets
// directory. It needs Docker: HOME_TEST_DOCKER=1 go test ./internal/secretfiles
func TestScriptOnNode(t *testing.T) {
	if os.Getenv("HOME_TEST_DOCKER") == "" {
		t.Skip("set HOME_TEST_DOCKER=1 to run against a Debian container")
	}
	moves := []Move{
		{From: "cf_dns_api_token", To: "network/cf_dns_api_token"},
		{From: "authelia", To: "network/authelia"},
		{From: "ab-cloudflared.env", To: "network/tunnel_token", Key: "TUNNEL_TOKEN"},
		{From: "ab-cloudflared.env", To: "network/quoted", Key: "QUOTED"},
		{From: "ab-cloudflared.env", To: "network/unquoted", Key: "UNQ"},
		{From: "ab-vault.env", To: "vault.env", Keys: []string{"ADMIN_TOKEN", "SSO"}},
	}
	bad := []Move{{From: "cf_dns_api_token", To: "x/cf"}, {From: "bad.env", To: "x/bad", Key: "BAD"}}
	const S, L = "/srv/secrets", "/srv/stacks"
	dir := t.TempDir()
	files := map[string]string{
		"status.sh":   Script("status", S, L, "network", moves, nil),
		"apply.sh":    Script("apply", S, L, "network", moves, nil),
		"cleanup.sh":  Script("cleanup", S, L, "network", moves, nil),
		"badapply.sh": Script("apply", S, L, "x", bad, nil),
		"undo.sh":     Script("undo", S, L, "x", nil, []string{"x/cf"}),
		"run.sh": `set -eu
apt-get -qq update >/dev/null && apt-get -qq install -y sudo >/dev/null
useradd -m -u 1000 dietpi && echo 'dietpi ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/d
useradd -u 10001 memos
mkdir -p /srv/secrets/authelia /srv/stacks/network /srv/stacks/dns
cd /srv/secrets
printf 'cftoken' > cf_dns_api_token; chown 1000:1000 cf_dns_api_token; chmod 600 cf_dns_api_token
printf 'jwt' > authelia/jwt_secret; printf '{}' > authelia/memos.json; chown 10001 authelia/memos.json; chmod 600 authelia/*
cat > ab-cloudflared.env <<'E'
TUNNEL_TOKEN=eyJhIjoi=
TUNNEL_TOKEN_FILE=/nope
QUOTED='va$lue #x'
UNQ=hello  #comment
BAD=has$dollar
E
printf 'TZ=Asia/Kolkata\nADMIN_TOKEN=adm\nSSO=s3\n' > ab-vault.env
chown 1000:1000 ab-cloudflared.env ab-vault.env; chmod 600 ab-cloudflared.env ab-vault.env
chown -R 1000:1000 /srv; chown 10001 authelia/memos.json
echo 'services: {t: {image: x, secrets: [a], env_file: /srv/secrets/vault.env}}
secrets: {a: {file: /srv/secrets/network/cf_dns_api_token}}' > /srv/stacks/network/compose.yaml
echo 'secrets: {a: {file: /srv/secrets/cf_dns_api_token}}  # still the old one' > /srv/stacks/dns/compose.yaml
cp /t/*.sh /home/dietpi/
su dietpi -c 'bash ~/status.sh' | sed 's/^/status1: /'
su dietpi -c 'bash ~/apply.sh' | sed 's/^/apply: /'
su dietpi -c 'bash ~/status.sh' | sed 's/^/status2: /'
for f in network/cf_dns_api_token network/tunnel_token network/quoted network/unquoted vault.env network/authelia/memos.json; do
  printf 'file %s %s %s [%s]\n' "$f" "$(stat -c %u "$f")" "$(stat -c %a "$f")" "$(cat "$f" | tr '\n' '|')"
done
printf 'dirmode %s %s\n' "$(stat -c %a network)" "$(stat -c %u network)"
su dietpi -c 'bash ~/cleanup.sh' | sed 's/^/cleanup: /'
ls -1 /srv/secrets | tr '\n' ' ' | sed 's/^/left: /'; echo
printf 'BAD=has$dollar\n' > /srv/secrets/bad.env; chown 1000:1000 /srv/secrets/bad.env
if su dietpi -c 'bash ~/badapply.sh' > /tmp/bad 2>&1; then echo "badapply: exit 0"; else echo "badapply: failed"; fi
sed 's/^/badapply: /' /tmp/bad
su dietpi -c 'bash ~/undo.sh' | sed 's/^/undo: /'
test -e /srv/secrets/x/cf && echo "undo: still there" || echo "undo: gone"
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("docker", "run", "--rm", "-v", dir+":/t:ro", "debian:bookworm-slim", "bash", "/t/run.sh").CombinedOutput()
	got := string(out)
	t.Log(got)
	if err != nil {
		t.Fatalf("container: %v", err)
	}
	for _, want := range []string{
		"status1: move 1 pending", "status1: move 6 pending",
		"apply: created network/cf_dns_api_token", "apply: created network/authelia", "apply: created vault.env",
		"status2: move 1 copied", "status2: move 2 copied", "status2: move 3 copied", "status2: move 6 copied",
		"file network/cf_dns_api_token 1000 600 [cftoken]",
		"file network/tunnel_token 1000 600 [eyJhIjoi=]",
		"file network/quoted 1000 600 [va$lue #x]",
		"file network/unquoted 1000 600 [hello]",
		"file vault.env 1000 600 [ADMIN_TOKEN=adm|SSO=s3|]",
		"file network/authelia/memos.json 10001 600 [{}]",
		"dirmode 700 1000",
		"cleanup: kept cf_dns_api_token used by dns/compose.yaml",
		"cleanup: removed authelia", "cleanup: removed ab-cloudflared.env", "cleanup: removed ab-vault.env",
		"badapply: failed", "BAD: unquoted value with $, move it by hand",
		"undo: removed x/cf", "undo: gone",
	} {
		if !strings.Contains(got, want+"\n") && !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(got, "left: cf_dns_api_token network vault.env \n") {
		t.Errorf("unexpected files left")
	}
}

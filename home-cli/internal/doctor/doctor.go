// Package doctor finds drift and risk on nodes: things that work today but
// will bite later (stale GitOps checkouts, unbounded logs, inline secrets,
// expiring certificates and Tailscale keys, containers that won't restart).
package doctor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/remote"
)

type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) Icon() string { return [...]string{"✓", "⚠", "✗"}[l] }

type Finding struct {
	Level       Level
	Title, Hint string
}

type Report struct {
	Node     string
	Err      error
	Findings []Finding
}

func (r *Report) add(l Level, title, hint string) {
	r.Findings = append(r.Findings, Finding{l, title, hint})
}

const script = `
top=$(git -C %[1]s rev-parse --show-toplevel 2>/dev/null)
if [ -n "$top" ]; then
  br=$(git -C "$top" rev-parse --abbrev-ref HEAD)
  echo "git_head=$(git -C "$top" rev-parse HEAD)"
  echo "git_branch=$br"
  echo "git_remote=$(timeout 20 git -C "$top" ls-remote origin "refs/heads/$br" 2>/dev/null | cut -f1)"
fi
echo "gitops=$(systemctl is-active gitops-agent 2>/dev/null || true)"
echo "ntp=$(timedatectl show -p NTPSynchronized --value 2>/dev/null)"
echo "logdriver=$(docker info --format '{{.LoggingDriver}}' 2>/dev/null)"
grep -q '"max-size"' /etc/docker/daemon.json 2>/dev/null && echo logmax=1 || echo logmax=0
for d in %[1]s/*/; do [ -f "$d/compose.yaml" ] && echo "stack=$(basename "$d")"; done
docker ps -aq | xargs -r docker inspect --format 'ctr={{.Name}}|{{.HostConfig.RestartPolicy.Name}}|{{.RestartCount}}|{{.State.Status}}|{{.Config.Image}}|{{index .Config.Labels "com.docker.compose.project"}}|{{json .Config.Labels}}|{{json .Config.Env}}'
( cd %[1]s && grep -HnE '^[[:space:]]*-?[[:space:]]*[A-Z0-9_]*(TOKEN|PASSWORD|PASSWD|SECRET|API_KEY|PRIVATE_KEY)[A-Z0-9_]*[[:space:]]*[:=][[:space:]]*[^[:space:]$"{]' */compose.yaml 2>/dev/null ) \
  | sed -E 's#^([^:]*:[0-9]+):[[:space:]]*-?[[:space:]]*([A-Z0-9_]+).*#inline=\1|\2#'
for c in $(docker ps --format '{{.Names}} {{.Image}}' | awk '/tailscale/{print $1}'); do
  echo "ts=$c|$(docker exec "$c" tailscale status --json 2>/dev/null | grep -m1 -oE '"KeyExpiry": *"[^"]*"' | cut -d'"' -f4)"
done
`

type ctr struct {
	name, restart, state, image, project string
	restarts                             int
	labels                               map[string]string
	env                                  []string
}

var hostRE = regexp.MustCompile("Host\\(`([^`]+)`\\)")

// Run diagnoses every node (or one) in parallel.
func Run(ctx context.Context, cfg *homecfg.Config, r *remote.Runner, only string) []Report {
	nodes, err := cfg.Ordered(only)
	if err != nil {
		return []Report{{Node: only, Err: err}}
	}
	reports := make([]Report, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, n homecfg.Node) {
			defer wg.Done()
			reports[i] = diagnose(ctx, n, r)
		}(i, n)
	}
	wg.Wait()
	return reports
}

func diagnose(ctx context.Context, n homecfg.Node, r *remote.Runner) Report {
	rep := Report{Node: n.Name}
	out, err := r.Output(ctx, n.Name, "bash -s <<'HOME_EOF'\n"+fmt.Sprintf(script, remote.Quote(n.StacksDir))+"HOME_EOF")
	if err != nil && out == "" {
		rep.Err = err
		return rep
	}
	kv := map[string]string{}
	var ctrs []ctr
	var stacks []string
	var inline [][2]string
	var ts [][2]string
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ctr":
			f := strings.SplitN(v, "|", 8)
			if len(f) < 8 {
				continue
			}
			c := ctr{name: strings.TrimPrefix(f[0], "/"), restart: f[1], state: f[3], image: f[4], project: f[5]}
			c.restarts, _ = strconv.Atoi(f[2])
			json.Unmarshal([]byte(f[6]), &c.labels)
			json.Unmarshal([]byte(f[7]), &c.env)
			ctrs = append(ctrs, c)
		case "stack":
			stacks = append(stacks, v)
		case "inline":
			loc, name, _ := strings.Cut(v, "|")
			if strings.HasSuffix(name, "_FILE") {
				continue // points at a secrets file: the recommended pattern
			}
			inline = append(inline, [2]string{loc, name})
		case "ts":
			c, exp, _ := strings.Cut(v, "|")
			ts = append(ts, [2]string{c, exp})
		default:
			kv[k] = v
		}
	}

	// GitOps
	switch {
	case kv["git_head"] == "":
		rep.add(Warn, "stacks directory is not a git checkout", "GitOps drift cannot be checked")
	case kv["git_remote"] == "":
		rep.add(Warn, "could not reach the git remote from the node", "check the node's deploy key / network")
	case kv["git_head"] != kv["git_remote"]:
		rep.add(Fail, fmt.Sprintf("checkout is behind origin/%s", kv["git_branch"]), "the GitOps agent is not deploying; check `journalctl -u gitops-agent`")
	default:
		rep.add(OK, "GitOps checkout matches origin/"+kv["git_branch"], "")
	}
	if g := kv["gitops"]; g != "" && g != "active" {
		rep.add(Fail, "gitops-agent is "+g, "sudo systemctl restart gitops-agent")
	}

	// Stacks vs containers
	known := map[string]bool{}
	for _, s := range stacks {
		known[s] = true
	}
	running := map[string]bool{}
	var strays []string
	for _, c := range ctrs {
		if c.project != "" {
			running[c.project] = true
		}
		if c.project != "" && !known[c.project] && c.state == "running" {
			strays = append(strays, c.name)
		}
	}
	var notRunning []string
	for _, s := range stacks {
		if !running[s] {
			notRunning = append(notRunning, s)
		}
	}
	if len(strays) > 0 {
		rep.add(Warn, "containers not from any stack in the repo: "+join(strays), "started by hand or from a removed stack; stop them or add a stack")
	}
	if len(notRunning) > 0 {
		rep.add(Warn, "stacks with no containers: "+join(notRunning), "intentionally stopped? otherwise `home stack start`")
	}

	// Containers
	var noRestart, looping []string
	for _, c := range ctrs {
		if c.state == "running" && (c.restart == "" || c.restart == "no") {
			noRestart = append(noRestart, c.name)
		}
		if c.restarts >= 5 {
			looping = append(looping, fmt.Sprintf("%s (%d restarts)", c.name, c.restarts))
		}
	}
	if len(noRestart) > 0 {
		rep.add(Warn, "no restart policy (won't come back after a reboot): "+join(noRestart), "add `restart: unless-stopped` to the service")
	}
	if len(looping) > 0 {
		rep.add(Warn, "containers that keep restarting: "+join(looping), "`home stack logs <node>/<stack>` to see why")
	}

	// Logs
	if kv["logdriver"] == "json-file" && kv["logmax"] != "1" {
		rep.add(Warn, "container logs grow without limit (no max-size in /etc/docker/daemon.json)",
			`add {"log-opts":{"max-size":"20m","max-file":"3"}} to daemon.json and restart docker`)
	}

	// Secrets written into compose files
	if len(inline) > 0 {
		var where []string
		for _, s := range inline {
			where = append(where, s[1]+" ("+s[0]+")")
		}
		rep.add(Warn, "secrets written directly in compose files: "+join(where), "move them to env files outside the repo (env_file:), then rotate them")
	}

	// Watchtower should only monitor; home applies updates.
	for _, c := range ctrs {
		if strings.Contains(c.image, "watchtower") && c.state == "running" {
			mon := false
			for _, e := range c.env {
				mon = mon || strings.EqualFold(e, "WATCHTOWER_MONITOR_ONLY=true")
			}
			if !mon {
				rep.add(Warn, "Watchtower applies updates on its own", "set WATCHTOWER_MONITOR_ONLY=true so updates go through `home stack update`")
			}
		}
	}

	// Time
	if kv["ntp"] == "no" {
		rep.add(Warn, "clock not synchronised (NTP)", "TLS, logs and schedules depend on it: check systemd-timesyncd")
	}

	// Tailscale keys
	for _, t := range ts {
		exp, err := time.Parse(time.RFC3339, t[1])
		if err != nil || t[1] == "" {
			continue
		}
		days := int(time.Until(exp).Hours() / 24)
		switch {
		case days < 0:
			rep.add(Fail, fmt.Sprintf("Tailscale key of %s expired %d days ago", t[0], -days), "re-authenticate, or disable key expiry for this node in the Tailscale admin")
		case days < 21:
			rep.add(Warn, fmt.Sprintf("Tailscale key of %s expires in %d days", t[0], days), "re-authenticate, or disable key expiry for this node in the Tailscale admin")
		}
	}

	// TLS certificates for every Traefik host on this node.
	hosts := map[string]bool{}
	for _, c := range ctrs {
		for k, v := range c.labels {
			if strings.HasPrefix(k, "traefik.http.routers.") && strings.HasSuffix(k, ".rule") {
				for _, m := range hostRE.FindAllStringSubmatch(v, -1) {
					hosts[m[1]] = true
				}
			}
		}
	}
	certs(ctx, &rep, hosts)

	sort.SliceStable(rep.Findings, func(i, j int) bool { return rep.Findings[i].Level > rep.Findings[j].Level })
	return rep
}

// certs checks expiry from this machine; hosts that don't resolve here
// (internal-only) are skipped.
func certs(ctx context.Context, rep *Report, hosts map[string]bool) {
	type res struct {
		host string
		days int
		err  error
	}
	var mu sync.Mutex
	var results []res
	var wg sync.WaitGroup
	for h := range hosts {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			if _, err := net.DefaultResolver.LookupHost(ctx, h); err != nil {
				return
			}
			d := &net.Dialer{Timeout: 6 * time.Second}
			conn, err := tls.DialWithDialer(d, "tcp", h+":443", &tls.Config{ServerName: h})
			r := res{host: h, err: err}
			if err == nil {
				r.days = int(time.Until(conn.ConnectionState().PeerCertificates[0].NotAfter).Hours() / 24)
				conn.Close()
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(h)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].host < results[j].host })
	var ok int
	for _, r := range results {
		switch {
		case r.err != nil:
			rep.add(Warn, "TLS check failed for "+r.host+": "+shorten(r.err.Error()), "")
		case r.days < 7:
			rep.add(Fail, fmt.Sprintf("certificate for %s expires in %d days", r.host, r.days), "check Traefik's ACME logs")
		case r.days < 21:
			rep.add(Warn, fmt.Sprintf("certificate for %s expires in %d days", r.host, r.days), "renewal normally happens 30 days before expiry; check Traefik's ACME logs")
		default:
			ok++
		}
	}
	if ok > 0 {
		rep.add(OK, fmt.Sprintf("%d TLS certificates valid for 3+ weeks", ok), "")
	}
}

func join(s []string) string {
	sort.Strings(s)
	if len(s) > 6 {
		return strings.Join(s[:6], ", ") + fmt.Sprintf(" … (+%d)", len(s)-6)
	}
	return strings.Join(s, ", ")
}

func shorten(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

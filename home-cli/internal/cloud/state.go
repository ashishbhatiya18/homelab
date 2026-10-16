package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/ashishbhatiya18/home/internal/cloudflare"
	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/remote"
)

// TunnelSuffix is the CNAME target domain of every tunnel.
const TunnelSuffix = ".cfargotunnel.com"

// State is the live picture `home cloud` works from.
type State struct {
	Tunnels []TunnelState
	Zones   []ZoneState
	// Connectors found on the nodes, by tunnel ID.
	Runners map[string][]Runner
	Routers []Router
	// Errors that left part of the picture empty (an unreachable node, a
	// zone the token can't read); everything else is still usable.
	Warnings []string
}

type TunnelState struct {
	cloudflare.Tunnel
	Config     *cloudflare.TunnelConfig // nil: locally managed or never configured
	Version    int
	Connectors []cloudflare.Connector
}

type ZoneState struct {
	cloudflare.Zone
	Records []cloudflare.DNSRecord
}

// Runner is a cloudflared container on a node.
type Runner struct {
	Node, Stack, Container, State, Health string
}

func (r Runner) String() string { return r.Node + "/" + r.Stack + " (" + r.Container + ")" }

// Router is a Traefik router declared in container labels.
type Router struct {
	Host, Node, Stack, Container, Name string
	Middlewares                        []string
}

// Protected reports a router behind the forward-auth middleware.
func (r Router) Protected() bool {
	for _, m := range r.Middlewares {
		if m == "auth" || strings.HasPrefix(m, "auth@") {
			return true
		}
	}
	return false
}

type GatherOpts struct {
	Connectors bool // per-tunnel connector details (one call per tunnel)
	DNS        bool // every zone's records
	Nodes      bool // cloudflared containers and Traefik routers over SSH
}

// Gather reads the account's tunnels (with their remote ingress) and, as
// asked, connectors, DNS and the nodes. Calls run in parallel.
func Gather(ctx context.Context, api *cloudflare.Client, accountID string, cfg *homecfg.Config, r *remote.Runner, o GatherOpts) (*State, error) {
	st := &State{Runners: map[string][]Runner{}}
	var mu sync.Mutex
	warn := func(f string, a ...any) {
		mu.Lock()
		st.Warnings = append(st.Warnings, fmt.Sprintf(f, a...))
		mu.Unlock()
	}
	var wg sync.WaitGroup
	if o.Nodes && cfg != nil && r != nil {
		for _, n := range cfg.Nodes {
			wg.Add(1)
			go func(n homecfg.Node) {
				defer wg.Done()
				runners, routers, err := discover(ctx, r, n.Name)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					st.Warnings = append(st.Warnings, fmt.Sprintf("%s: %v", n.Name, err))
					return
				}
				for id, rs := range runners {
					st.Runners[id] = append(st.Runners[id], rs...)
				}
				st.Routers = append(st.Routers, routers...)
			}(n)
		}
	}
	if o.DNS {
		wg.Add(1)
		go func() {
			defer wg.Done()
			zones, err := api.Zones(ctx, accountID)
			if err != nil {
				warn("zones: %v", err)
				return
			}
			zs := make([]ZoneState, len(zones))
			var zwg sync.WaitGroup
			for i, z := range zones {
				zs[i].Zone = z
				zwg.Add(1)
				go func(i int) {
					defer zwg.Done()
					recs, err := api.DNSRecords(ctx, zs[i].ID)
					if err != nil {
						warn("%s: DNS records: %v", zs[i].Name, err)
					}
					zs[i].Records = recs
				}(i)
			}
			zwg.Wait()
			sort.Slice(zs, func(i, j int) bool { return zs[i].Name < zs[j].Name })
			mu.Lock()
			st.Zones = zs
			mu.Unlock()
		}()
	}

	tunnels, err := api.Tunnels(ctx, accountID)
	if err != nil {
		wg.Wait()
		return nil, err
	}
	sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].Name < tunnels[j].Name })
	st.Tunnels = make([]TunnelState, len(tunnels))
	for i, t := range tunnels {
		st.Tunnels[i].Tunnel = t
		if t.Remote() {
			wg.Add(1)
			go func(ts *TunnelState) {
				defer wg.Done()
				c, err := api.TunnelConfig(ctx, accountID, ts.ID)
				if err != nil {
					warn("%s: ingress: %v", ts.Name, err)
					return
				}
				ts.Config, ts.Version = c.Config, c.Version
			}(&st.Tunnels[i])
		}
		if o.Connectors {
			wg.Add(1)
			go func(ts *TunnelState) {
				defer wg.Done()
				cs, err := api.Connectors(ctx, accountID, ts.ID)
				if err != nil {
					warn("%s: connectors: %v", ts.Name, err)
					return
				}
				ts.Connectors = cs
			}(&st.Tunnels[i])
		}
	}
	wg.Wait()
	sort.Slice(st.Routers, func(i, j int) bool { return st.Routers[i].Host < st.Routers[j].Host })
	sort.Strings(st.Warnings)
	return st, nil
}

// Tunnel finds a tunnel by name or ID.
func (st *State) Tunnel(nameOrID string) *TunnelState {
	for i := range st.Tunnels {
		if st.Tunnels[i].Name == nameOrID || st.Tunnels[i].ID == nameOrID {
			return &st.Tunnels[i]
		}
	}
	return nil
}

// ZoneFor returns the zone a hostname belongs to (the longest match).
func (st *State) ZoneFor(host string) *ZoneState {
	var best *ZoneState
	for i := range st.Zones {
		z := &st.Zones[i]
		if (host == z.Name || strings.HasSuffix(host, "."+z.Name)) && (best == nil || len(z.Name) > len(best.Name)) {
			best = z
		}
	}
	return best
}

// Records returns the DNS records named host.
func (st *State) Records(host string) []cloudflare.DNSRecord {
	z := st.ZoneFor(host)
	if z == nil {
		return nil
	}
	var out []cloudflare.DNSRecord
	for _, r := range z.Records {
		if r.Name == host {
			out = append(out, r)
		}
	}
	return out
}

// TunnelOfRecord returns the tunnel ID a record points at, if it is a tunnel
// CNAME.
func TunnelOfRecord(r cloudflare.DNSRecord) (string, bool) {
	if r.Type != "CNAME" || !strings.HasSuffix(r.Content, TunnelSuffix) {
		return "", false
	}
	return strings.TrimSuffix(r.Content, TunnelSuffix), true
}

// RoutersFor returns the Traefik routers for host.
func (st *State) RoutersFor(host string) []Router {
	var out []Router
	for _, r := range st.Routers {
		if r.Host == host {
			out = append(out, r)
		}
	}
	return out
}

// ---- nodes ---------------------------------------------------------------------

// discoverScript prints one line per cloudflared container with the tunnel ID
// from its token — decoded on the node, so the token itself never leaves it —
// and one line per container with Traefik router labels. Containers are
// matched on their configured image (.Config.Image): `docker ps` shows only an
// image ID once the tag has moved to a newer pull.
const discoverScript = `
tunnel_id() { tr -d '[:space:]' | base64 -d 2>/dev/null | grep -oE '"t" *: *"[0-9a-fA-F-]+"' | grep -oE '[0-9a-fA-F-]{36}'; }
docker ps -aq | xargs -r docker inspect --format '{{.Name}}|{{.Config.Image}}' | awk -F'|' '$2 ~ /cloudflared/ {sub("^/", "", $1); print $1}' | while read -r c; do
  env=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$c")
  args=$(docker inspect --format '{{range .Args}}{{println .}}{{end}}' "$c")
  tok=$(printf '%s\n' "$env" | sed -n 's/^TUNNEL_TOKEN=//p')
  [ -z "$tok" ] && tok=$(printf '%s\n' "$args" | sed -n '/^--token$/{n;p;}; s/^--token=//p' | head -n1)
  f=$(printf '%s\n' "$env" | sed -n 's/^TUNNEL_TOKEN_FILE=//p')
  if [ -z "$tok" ] && [ -n "$f" ]; then
    src=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "'"$f"'"}}{{.Source}}{{end}}{{end}}' "$c")
    if [ -n "$src" ] && [ -r "$src" ]; then tok=$(cat "$src"); else tok=$(docker cp "$c:$f" - 2>/dev/null | tar -xO 2>/dev/null); fi
  fi
  id=$(printf '%s' "$tok" | tunnel_id)
  echo "cfd=$c|$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$c")|$id"
done
docker ps -q | xargs -r docker inspect --format 'labels={{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{json .Config.Labels}}' | grep 'traefik\.http\.routers\.'
true
`

func discover(ctx context.Context, r *remote.Runner, node string) (map[string][]Runner, []Router, error) {
	out, err := r.Output(ctx, node, "bash -s <<'HOME_EOF'\n"+discoverScript+"HOME_EOF")
	if err != nil && out == "" {
		return nil, nil, err
	}
	runners, routers := parseDiscover(node, out)
	return runners, routers, nil
}

var hostRE = regexp.MustCompile("Host\\(`([^`]+)`\\)")

func parseDiscover(node, out string) (map[string][]Runner, []Router) {
	runners := map[string][]Runner{}
	var routers []Router
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "cfd":
			f := strings.Split(v, "|")
			if len(f) != 5 {
				continue
			}
			id := strings.ToLower(f[4])
			runners[id] = append(runners[id], Runner{Node: node, Container: f[0], Stack: f[1], State: f[2], Health: f[3]})
		case "labels":
			f := strings.SplitN(v, "|", 3)
			if len(f) != 3 {
				continue
			}
			var labels map[string]string
			if json.Unmarshal([]byte(f[2]), &labels) != nil {
				continue
			}
			ctr := strings.TrimPrefix(f[0], "/")
			for key, rule := range labels {
				name, ok := strings.CutPrefix(key, "traefik.http.routers.")
				if !ok {
					continue
				}
				name, ok = strings.CutSuffix(name, ".rule")
				if !ok {
					continue
				}
				var mws []string
				for _, m := range strings.Split(labels["traefik.http.routers."+name+".middlewares"], ",") {
					if m = strings.TrimSpace(m); m != "" {
						mws = append(mws, m)
					}
				}
				for _, m := range hostRE.FindAllStringSubmatch(rule, -1) {
					routers = append(routers, Router{Host: m[1], Node: node, Stack: f[1], Container: ctr, Name: name, Middlewares: mws})
				}
			}
		}
	}
	return runners, routers
}

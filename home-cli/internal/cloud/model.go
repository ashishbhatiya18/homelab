// Package cloud is the declarative side of `home cloud`: one YAML file per
// Cloudflare Tunnel (its ingress rules, in cloudflared's own field names) and
// one per DNS zone, kept in git. It also gathers the live state they are
// compared with — the API, the cloudflared containers on the nodes and the
// Traefik routers behind them — and checks routes end to end.
//
// A route that points a hostname at a tunnel needs a proxied CNAME to
// <tunnel-id>.cfargotunnel.com. Those records are implied by the tunnel file
// and are not repeated in the zone file.
package cloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ashishbhatiya18/home/internal/cloudflare"
	"gopkg.in/yaml.v3"
)

// HostVar in a default originRequest value stands for the route's hostname.
const HostVar = "{host}"

// TunnelFile is tunnels/<name>.yaml.
type TunnelFile struct {
	Tunnel string `yaml:"tunnel"`
	ID     string `yaml:"id,omitempty"`
	// Where the connector runs, for token installs and restarts.
	Node      string `yaml:"node,omitempty"`
	Stack     string `yaml:"stack,omitempty"`
	Container string `yaml:"container,omitempty"`
	// Secret is the node path holding the token: a file with just the token
	// (TUNNEL_TOKEN_FILE) or an env file with a TUNNEL_TOKEN= line.
	Secret string `yaml:"secret,omitempty"`
	// Local: the ingress rules live in cloudflared's own config file and are
	// not managed here.
	Local         bool           `yaml:"local,omitempty"`
	Defaults      *Defaults      `yaml:"defaults,omitempty"`
	OriginRequest map[string]any `yaml:"originRequest,omitempty"`
	WarpRouting   map[string]any `yaml:"warp-routing,omitempty"`
	Ingress       []Rule         `yaml:"ingress,omitempty"`
}

// Defaults apply to bare-hostname rules; {host} in an originRequest string is
// replaced by the hostname.
type Defaults struct {
	Service       string         `yaml:"service"`
	OriginRequest map[string]any `yaml:"originRequest,omitempty"`
}

// Rule is an ingress rule. Written as a bare hostname it takes the defaults;
// written in full it stands alone (no merging with the defaults).
type Rule struct {
	Hostname      string         `yaml:"hostname,omitempty"`
	Path          string         `yaml:"path,omitempty"`
	Service       string         `yaml:"service,omitempty"`
	OriginRequest map[string]any `yaml:"originRequest,omitempty"`
	// Short: the rule was (or will be) written as a bare hostname.
	Short bool `yaml:"-"`
}

func (r *Rule) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*r = Rule{Hostname: n.Value, Short: true}
		return nil
	}
	type plain Rule
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*r = Rule(p)
	return nil
}

func (r Rule) MarshalYAML() (any, error) {
	if r.Short {
		return r.Hostname, nil
	}
	type plain Rule
	return plain(r), nil
}

// Expand turns the file into the configuration the API takes.
func (f *TunnelFile) Expand() (cloudflare.TunnelConfig, error) {
	cfg := cloudflare.TunnelConfig{OriginRequest: f.OriginRequest, WarpRouting: f.WarpRouting}
	seen := map[string]bool{}
	for i, r := range f.Ingress {
		key := r.Hostname + r.Path
		if r.Hostname != "" && seen[key] {
			return cfg, fmt.Errorf("%s: %s is listed twice", f.Tunnel, key)
		}
		seen[key] = true
		out := cloudflare.IngressRule{Hostname: r.Hostname, Path: r.Path, Service: r.Service, OriginRequest: r.OriginRequest}
		if r.Short {
			if f.Defaults == nil || f.Defaults.Service == "" {
				return cfg, fmt.Errorf("%s: %s has no service and the file sets no defaults", f.Tunnel, r.Hostname)
			}
			out.Service, out.OriginRequest = f.Defaults.Service, fill(f.Defaults.OriginRequest, r.Hostname)
		}
		if out.Service == "" {
			return cfg, fmt.Errorf("%s: rule %d (%s) has no service", f.Tunnel, i+1, r.Hostname)
		}
		cfg.Ingress = append(cfg.Ingress, out)
	}
	if n := len(cfg.Ingress); n > 0 && cfg.Ingress[n-1].Hostname != "" {
		return cfg, fmt.Errorf("%s: the last ingress rule must match everything (e.g. `- service: http_status:404`)", f.Tunnel)
	}
	return cfg, nil
}

// Hostnames are the hostnames the file routes, in file order.
func (f *TunnelFile) Hostnames() []string {
	var hs []string
	for _, r := range f.Ingress {
		if r.Hostname != "" {
			hs = append(hs, r.Hostname)
		}
	}
	return hs
}

// fill substitutes host for {host} in the top-level string values.
func fill(m map[string]any, host string) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			v = strings.ReplaceAll(s, HostVar, host)
		}
		out[k] = v
	}
	return out
}

// template is the reverse of fill: values equal to host become {host}.
func template(m map[string]any, host string) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok && s == host {
			v = HostVar
		}
		out[k] = v
	}
	return out
}

// canon is a comparable form of a JSON-ish value (numbers from YAML and from
// JSON compare equal, map key order doesn't matter).
func canon(v any) string {
	b, _ := json.Marshal(v)
	var x any
	_ = json.Unmarshal(b, &x)
	b, _ = json.Marshal(x)
	return string(b)
}

// Compact builds a tunnel file from a live configuration: when two or more
// rules share a service and an originRequest that differs only by their own
// hostname, that pair becomes the defaults and those rules bare hostnames.
func Compact(name string, cfg *cloudflare.TunnelConfig) TunnelFile {
	f := TunnelFile{Tunnel: name}
	if cfg == nil {
		return f
	}
	f.OriginRequest, f.WarpRouting = cfg.OriginRequest, cfg.WarpRouting
	if v, ok := f.WarpRouting["enabled"].(bool); ok && !v && len(f.WarpRouting) == 1 {
		f.WarpRouting = nil // the API's default; noise in the file
	}

	type cand struct {
		d     Defaults
		count int
		first int
	}
	cands := map[string]*cand{}
	for i, r := range cfg.Ingress {
		if r.Hostname == "" || r.Path != "" {
			continue
		}
		d := Defaults{Service: r.Service, OriginRequest: template(r.OriginRequest, r.Hostname)}
		k := canon(d)
		if cands[k] == nil {
			cands[k] = &cand{d: d, first: i}
		}
		cands[k].count++
	}
	var best *cand
	for _, c := range cands {
		if c.count >= 2 && (best == nil || c.count > best.count || (c.count == best.count && c.first < best.first)) {
			best = c
		}
	}
	var bestKey string
	if best != nil {
		f.Defaults = &best.d
		bestKey = canon(best.d)
	}
	for _, r := range cfg.Ingress {
		rule := Rule{Hostname: r.Hostname, Path: r.Path, Service: r.Service, OriginRequest: r.OriginRequest}
		if best != nil && r.Hostname != "" && r.Path == "" &&
			canon(Defaults{Service: r.Service, OriginRequest: template(r.OriginRequest, r.Hostname)}) == bestKey {
			rule = Rule{Hostname: r.Hostname, Short: true}
		}
		f.Ingress = append(f.Ingress, rule)
	}
	return f
}

// ZoneFile is zones/<zone>.yaml: the zone's DNS records other than the CNAMEs
// implied by tunnel routes.
type ZoneFile struct {
	Zone string `yaml:"zone"`
	ID   string `yaml:"id,omitempty"`
	// Ignore lists record names (relative, shell patterns) that something
	// else manages; they are neither exported nor ever changed. DefaultIgnore
	// always applies.
	Ignore  []string `yaml:"ignore,omitempty"`
	Records []Record `yaml:"records"`
}

// DefaultIgnore: ACME DNS-01 challenge records come and go with certificate
// renewals.
var DefaultIgnore = []string{"_acme-challenge", "_acme-challenge.*"}

// Ignored reports whether a relative record name is someone else's.
func (z ZoneFile) Ignored(rel string) bool {
	for _, pat := range append(append([]string{}, DefaultIgnore...), z.Ignore...) {
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
	}
	return false
}

// Record is a DNS record. Name is relative to the zone ("@" is the apex).
type Record struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`
	Content  string `yaml:"content"`
	Proxied  bool   `yaml:"proxied,omitempty"`
	TTL      int    `yaml:"ttl,omitempty"` // omitted: automatic
	Priority *int   `yaml:"priority,omitempty"`
	Comment  string `yaml:"comment,omitempty"`
	// ReadOnly records are managed by Cloudflare itself (e.g. Email Routing)
	// and are listed for completeness only.
	ReadOnly bool `yaml:"read_only,omitempty"`
}

func (r Record) MarshalYAML() (any, error) {
	type plain Record
	n := &yaml.Node{}
	if err := n.Encode(plain(r)); err != nil {
		return nil, err
	}
	n.Style = yaml.FlowStyle
	return n, nil
}

// RelName makes an absolute record name relative to zone.
func RelName(name, zone string) string {
	if name == zone {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone)
}

// AbsName is the reverse of RelName.
func AbsName(name, zone string) string {
	if name == "@" || name == "" {
		return zone
	}
	return name + "." + zone
}

func SortRecords(rs []Record) {
	order := func(n string) string {
		if n == "@" {
			return ""
		}
		return n
	}
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if order(a.Name) != order(b.Name) {
			return order(a.Name) < order(b.Name)
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Content < b.Content
	})
}

// ---- files on disk -----------------------------------------------------------------

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func fileName(name string) string { return unsafeName.ReplaceAllString(name, "_") + ".yaml" }

func TunnelPath(dir, tunnel string) string { return filepath.Join(dir, "tunnels", fileName(tunnel)) }
func ZonePath(dir, zone string) string     { return filepath.Join(dir, "zones", fileName(zone)) }

const tunnelHeader = `# Cloudflare Tunnel %s — managed by ` + "`home cloud`" + `.
# Ingress rules use cloudflared's field names; a bare hostname takes the
# defaults ({host} = that hostname). The last rule must match everything.
# Each hostname also gets a proxied CNAME to the tunnel; it is not repeated in
# the zone file.
`

const zoneHeader = `# DNS for %s — managed by ` + "`home cloud`" + `.
# Names are relative to the zone ("@" is the apex); ttl omitted = automatic.
# ignore: names (shell patterns) managed elsewhere; _acme-challenge* always is.
# CNAMEs for tunnel routes are implied by tunnels/*.yaml and not listed here.
`

func write(path, header string, v any) error {
	var buf bytes.Buffer
	buf.WriteString(header + "\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func SaveTunnel(dir string, f TunnelFile) (string, error) {
	p := TunnelPath(dir, f.Tunnel)
	return p, write(p, fmt.Sprintf(tunnelHeader, f.Tunnel), f)
}

func SaveZone(dir string, z ZoneFile) (string, error) {
	p := ZonePath(dir, z.Zone)
	return p, write(p, fmt.Sprintf(zoneHeader, z.Zone), z)
}

// Files is everything under a cloud directory.
type Files struct {
	Tunnels []TunnelFile
	Zones   []ZoneFile
}

// Load reads every tunnels/*.yaml and zones/*.yaml under dir; a missing
// directory is empty, not an error.
func Load(dir string) (*Files, error) {
	var fs Files
	var errs []error
	read := func(sub string, each func(path string, b []byte) error) {
		paths, _ := filepath.Glob(filepath.Join(dir, sub, "*.yaml"))
		sort.Strings(paths)
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err == nil {
				err = each(p, b)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
			}
		}
	}
	read("tunnels", func(p string, b []byte) error {
		var f TunnelFile
		if err := yaml.Unmarshal(b, &f); err != nil {
			return err
		}
		if f.Tunnel == "" {
			return errors.New("missing `tunnel:`")
		}
		fs.Tunnels = append(fs.Tunnels, f)
		return nil
	})
	read("zones", func(p string, b []byte) error {
		var z ZoneFile
		if err := yaml.Unmarshal(b, &z); err != nil {
			return err
		}
		if z.Zone == "" {
			return errors.New("missing `zone:`")
		}
		fs.Zones = append(fs.Zones, z)
		return nil
	})
	return &fs, errors.Join(errs...)
}

// Zone returns the file for a zone, if any.
func (fs *Files) Zone(name string) *ZoneFile {
	for i := range fs.Zones {
		if fs.Zones[i].Zone == name {
			return &fs.Zones[i]
		}
	}
	return nil
}

// Tunnel returns the file for a tunnel name, if any.
func (fs *Files) Tunnel(name string) *TunnelFile {
	for i := range fs.Tunnels {
		if fs.Tunnels[i].Tunnel == name {
			return &fs.Tunnels[i]
		}
	}
	return nil
}

package cloud

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ashishbhatiya18/home/internal/cloudflare"
)

// Desired is the state to reach, from the files or from a snapshot.
type Desired struct {
	Tunnels []DesiredTunnel
	Zones   []DesiredZone
}

type DesiredTunnel struct {
	ID, Name string
	Config   cloudflare.TunnelConfig
}

// DesiredZone holds absolute-name records. Full zones (with a zone file) are
// managed whole; otherwise only the records named in Names are.
type DesiredZone struct {
	ID, Name string
	Full     bool
	Names    map[string]bool
	Ignore   []string
	Records  []cloudflare.DNSRecord
}

func (z DesiredZone) manages(r cloudflare.DNSRecord) bool {
	if r.ReadOnly() || (ZoneFile{Ignore: z.Ignore}).Ignored(RelName(r.Name, z.Name)) {
		return false
	}
	return z.Full || z.Names[strings.ToLower(r.Name)]
}

// FromFiles resolves the files against the live state: tunnel files to their
// live tunnels, zone files to zones, and each routed hostname to the proxied
// CNAME it needs.
func FromFiles(fs *Files, st *State) (*Desired, []string, error) {
	var d Desired
	var notes []string
	var errs []error
	managedTunnels := map[string]bool{}
	implied := map[string]string{} // hostname -> tunnel ID
	for i := range fs.Tunnels {
		f := &fs.Tunnels[i]
		t := st.Tunnel(f.ID)
		if t == nil || f.ID == "" {
			t = st.Tunnel(f.Tunnel)
		}
		if t == nil {
			errs = append(errs, fmt.Errorf("tunnel %s doesn't exist in Cloudflare — create it with `home tunnel create %s`", f.Tunnel, f.Tunnel))
			continue
		}
		if f.Local || len(f.Ingress) == 0 {
			continue // ingress not managed here
		}
		if !t.Remote() {
			errs = append(errs, fmt.Errorf("tunnel %s reads its ingress from cloudflared's local config file; set `local: true` or migrate it in the dashboard", f.Tunnel))
			continue
		}
		cfg, err := f.Expand()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		managedTunnels[t.ID] = true
		d.Tunnels = append(d.Tunnels, DesiredTunnel{ID: t.ID, Name: t.Name, Config: cfg})
		for _, h := range f.Hostnames() {
			if other, ok := implied[h]; ok && other != t.ID {
				errs = append(errs, fmt.Errorf("%s is routed by two tunnels", h))
			}
			implied[h] = t.ID
		}
	}
	for _, t := range st.Tunnels {
		if fs.Tunnel(t.Name) == nil {
			found := false
			for _, f := range fs.Tunnels {
				found = found || f.ID == t.ID
			}
			if !found {
				notes = append(notes, fmt.Sprintf("tunnel %s has no file; it is left alone", t.Name))
			}
		}
	}

	zones := map[string]*DesiredZone{} // zone name -> desired
	var order []string
	for _, zf := range fs.Zones {
		var zs *ZoneState
		for i := range st.Zones {
			if st.Zones[i].Name == zf.Zone {
				zs = &st.Zones[i]
			}
		}
		if zs == nil {
			errs = append(errs, fmt.Errorf("zone %s is not in the account (or the token can't read it)", zf.Zone))
			continue
		}
		dz := &DesiredZone{ID: zs.ID, Name: zs.Name, Full: true, Ignore: zf.Ignore}
		for _, r := range zf.Records {
			if r.ReadOnly {
				continue
			}
			dz.Records = append(dz.Records, cloudflare.DNSRecord{Name: AbsName(r.Name, zf.Zone), Type: strings.ToUpper(r.Type),
				Content: r.Content, Proxied: r.Proxied, TTL: r.TTL, Priority: r.Priority, Comment: r.Comment})
		}
		zones[zs.Name] = dz
		order = append(order, zs.Name)
	}
	hosts := make([]string, 0, len(implied))
	for h := range implied {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		zs := st.ZoneFor(h)
		if zs == nil {
			notes = append(notes, fmt.Sprintf("%s: its zone isn't in the account; add its DNS record elsewhere", h))
			continue
		}
		dz := zones[zs.Name]
		if dz == nil {
			dz = &DesiredZone{ID: zs.ID, Name: zs.Name, Names: map[string]bool{}}
			zones[zs.Name] = dz
			order = append(order, zs.Name)
		}
		if dz.Names != nil {
			dz.Names[h] = true
		}
		dz.Records = append(dz.Records, cloudflare.DNSRecord{Name: h, Type: "CNAME", Content: implied[h] + TunnelSuffix, Proxied: true})
	}
	// In zones without a file, tunnel CNAMEs of managed tunnels are ours too,
	// so a removed route takes its record with it.
	for _, zs := range st.Zones {
		for _, r := range zs.Records {
			if id, ok := TunnelOfRecord(r); ok && managedTunnels[id] {
				if dz := zones[zs.Name]; dz != nil && !dz.Full {
					dz.Names[strings.ToLower(r.Name)] = true
				} else if dz == nil {
					zones[zs.Name] = &DesiredZone{ID: zs.ID, Name: zs.Name, Names: map[string]bool{strings.ToLower(r.Name): true}}
					order = append(order, zs.Name)
				}
			}
		}
	}
	for _, n := range order {
		d.Zones = append(d.Zones, *zones[n])
	}
	return &d, notes, errors.Join(errs...)
}

// ---- plan ----------------------------------------------------------------------

type Op string

const (
	Create Op = "+"
	Update Op = "~"
	Delete Op = "-"
)

// Change is one API write. Tunnel changes replace the tunnel's whole config;
// Stage orders them around the DNS changes (see Plan).
type Change struct {
	Op    Op
	Stage int // 1: tunnel configs with additions, 2: DNS, 3: DNS deletes, 4: tunnel configs final
	// Tunnel config
	TunnelID, Tunnel string
	Config           *cloudflare.TunnelConfig
	Lines            []string // human-readable rule changes
	// DNS
	ZoneID, Zone string
	Old, New     *cloudflare.DNSRecord
}

func (c Change) String() string {
	if c.Config != nil {
		return "tunnel " + c.Tunnel + ": ingress\n    " + strings.Join(c.Lines, "\n    ")
	}
	r := c.New
	if r == nil {
		r = c.Old
	}
	s := fmt.Sprintf("%s %s %s %s", c.Op, RelName(r.Name, c.Zone), r.Type, recordValue(*r))
	if c.Op == Update {
		s = fmt.Sprintf("~ %s %s %s → %s", RelName(r.Name, c.Zone), r.Type, recordValue(*c.Old), recordValue(*c.New))
	}
	return "dns " + c.Zone + ": " + s
}

func recordValue(r cloudflare.DNSRecord) string {
	s := r.Content
	if r.Priority != nil {
		s = fmt.Sprintf("%d %s", *r.Priority, s)
	}
	if r.Proxied {
		s += " (proxied)"
	}
	if r.TTL > 1 && !r.Proxied {
		s += fmt.Sprintf(" ttl %d", r.TTL)
	}
	if r.Comment != "" {
		s += " # " + r.Comment
	}
	return s
}

// Plan lists the writes that turn live into d. The order avoids gaps: a
// tunnel first gains its new rules while keeping the ones being removed,
// then DNS changes (deletes last), then the tunnel drops the old rules — so a
// hostname moving between tunnels never meets a catch-all 404.
func Plan(d *Desired, st *State) []Change {
	var changes []Change
	for _, dt := range d.Tunnels {
		live := st.Tunnel(dt.ID)
		var lc cloudflare.TunnelConfig
		if live != nil && live.Config != nil {
			lc = *live.Config
		}
		changes = append(changes, tunnelChanges(dt, lc)...)
	}
	for _, dz := range d.Zones {
		var live []cloudflare.DNSRecord
		for _, zs := range st.Zones {
			if zs.ID == dz.ID {
				for _, r := range zs.Records {
					if dz.manages(r) {
						live = append(live, r)
					}
				}
			}
		}
		changes = append(changes, dnsChanges(dz, live)...)
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Stage < changes[j].Stage })
	return changes
}

func ruleKey(r cloudflare.IngressRule) string {
	if r.Hostname == "" && r.Path == "" {
		return "(everything else)"
	}
	return r.Hostname + r.Path
}

func ruleDesc(r cloudflare.IngressRule) string {
	s := r.Service
	if o := originDesc(r); o != "" {
		s += " (" + o + ")"
	}
	return s
}

func originDesc(r cloudflare.IngressRule) string {
	m := clean(r.OriginRequest)
	if len(m) == 0 {
		return ""
	}
	var parts []string
	if m["httpHostHeader"] == r.Hostname && m["originServerName"] == r.Hostname {
		parts = append(parts, "host+sni")
		delete(m, "httpHostHeader")
		delete(m, "originServerName")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// clean copies m without empty values, which the API treats as unset.
func clean(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if v == nil || v == "" {
			continue
		}
		if mm, ok := v.(map[string]any); ok && len(mm) == 0 {
			continue
		}
		out[k] = v
	}
	return out
}

func normRule(r cloudflare.IngressRule) cloudflare.IngressRule {
	r.OriginRequest = clean(r.OriginRequest)
	if len(r.OriginRequest) == 0 {
		r.OriginRequest = nil
	}
	return r
}

func normConfig(c cloudflare.TunnelConfig) string {
	n := cloudflare.TunnelConfig{OriginRequest: clean(c.OriginRequest), WarpRouting: clean(c.WarpRouting)}
	if v, ok := n.WarpRouting["enabled"].(bool); ok && !v {
		delete(n.WarpRouting, "enabled")
	}
	for _, r := range c.Ingress {
		n.Ingress = append(n.Ingress, normRule(r))
	}
	return canon(n)
}

func tunnelChanges(dt DesiredTunnel, live cloudflare.TunnelConfig) []Change {
	if normConfig(dt.Config) == normConfig(live) {
		return nil
	}
	liveBy := map[string]cloudflare.IngressRule{}
	for _, r := range live.Ingress {
		liveBy[ruleKey(r)] = r
	}
	wantBy := map[string]bool{}
	var lines []string
	added := false
	for _, r := range dt.Config.Ingress {
		k := ruleKey(r)
		wantBy[k] = true
		old, ok := liveBy[k]
		switch {
		case !ok:
			lines = append(lines, "+ "+k+" → "+ruleDesc(r))
			added = true
		case canon(normRule(old)) != canon(normRule(r)):
			lines = append(lines, "~ "+k+": "+ruleDesc(old)+" → "+ruleDesc(r))
			added = true
		}
	}
	var removed []cloudflare.IngressRule
	for _, r := range live.Ingress {
		if !wantBy[ruleKey(r)] {
			lines = append(lines, "- "+ruleKey(r))
			removed = append(removed, r)
		}
	}
	if canon(clean(dt.Config.OriginRequest)) != canon(clean(live.OriginRequest)) {
		lines = append(lines, fmt.Sprintf("~ tunnel-wide originRequest: %v → %v", clean(live.OriginRequest), clean(dt.Config.OriginRequest)))
		added = true
	}
	if len(lines) == 0 {
		lines = append(lines, "~ rule order or warp-routing")
		added = true
	}
	final := dt.Config
	if len(removed) == 0 || !added || len(dt.Config.Ingress) == 0 {
		return []Change{{Op: Update, Stage: map[bool]int{true: 1, false: 4}[added], TunnelID: dt.ID, Tunnel: dt.Name, Config: &final, Lines: lines}}
	}
	// Two steps: everything new plus the rules on their way out, then final.
	interim := dt.Config
	in := append([]cloudflare.IngressRule{}, dt.Config.Ingress[:len(dt.Config.Ingress)-1]...)
	for _, r := range removed {
		if r.Hostname != "" {
			in = append(in, r)
		}
	}
	interim.Ingress = append(in, dt.Config.Ingress[len(dt.Config.Ingress)-1])
	return []Change{
		{Op: Update, Stage: 1, TunnelID: dt.ID, Tunnel: dt.Name, Config: &interim, Lines: lines},
		{Op: Update, Stage: 4, TunnelID: dt.ID, Tunnel: dt.Name, Config: &final, Lines: []string{"(drop the removed rules)"}},
	}
}

// normContent makes TXT values comparable whether or not Cloudflare shows
// them quoted.
func normContent(r cloudflare.DNSRecord) string {
	c := strings.TrimSuffix(strings.TrimSpace(r.Content), ".")
	if r.Type == "TXT" && len(c) >= 2 && c[0] == '"' && c[len(c)-1] == '"' && !strings.Contains(c[1:len(c)-1], `"`) {
		c = c[1 : len(c)-1]
	}
	if r.Type != "TXT" {
		c = strings.ToLower(c)
	}
	return c
}

func sameRecord(a, b cloudflare.DNSRecord) bool {
	ttl := func(r cloudflare.DNSRecord) int {
		if r.Proxied || r.TTL == 0 {
			return 1
		}
		return r.TTL
	}
	prio := func(r cloudflare.DNSRecord) int {
		if r.Priority == nil {
			return -1
		}
		return *r.Priority
	}
	return normContent(a) == normContent(b) && a.Proxied == b.Proxied && ttl(a) == ttl(b) && prio(a) == prio(b) && a.Comment == b.Comment
}

func dnsChanges(dz DesiredZone, live []cloudflare.DNSRecord) []Change {
	type key struct{ name, typ string }
	want := map[key][]cloudflare.DNSRecord{}
	have := map[key][]cloudflare.DNSRecord{}
	var keys []key
	add := func(m map[key][]cloudflare.DNSRecord, r cloudflare.DNSRecord) {
		k := key{strings.ToLower(r.Name), strings.ToUpper(r.Type)}
		if _, ok := want[k]; !ok {
			if _, ok := have[k]; !ok {
				keys = append(keys, k)
			}
		}
		m[k] = append(m[k], r)
	}
	for _, r := range dz.Records {
		add(want, r)
	}
	for _, r := range live {
		add(have, r)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		return keys[i].typ < keys[j].typ
	})
	createNames := map[string]bool{}
	var changes []Change
	for _, k := range keys {
		w, h := append([]cloudflare.DNSRecord{}, want[k]...), append([]cloudflare.DNSRecord{}, have[k]...)
		// Identical records stay.
		for i := 0; i < len(w); i++ {
			for j := 0; j < len(h); j++ {
				if sameRecord(w[i], h[j]) {
					w, h = append(w[:i], w[i+1:]...), append(h[:j], h[j+1:]...)
					i--
					break
				}
			}
		}
		// Same content, other settings: update in place; then pair the rest.
		pair := func(match func(a, b cloudflare.DNSRecord) bool) {
			for i := 0; i < len(w); i++ {
				for j := 0; j < len(h); j++ {
					if match(w[i], h[j]) {
						old, nw := h[j], w[i]
						nw.Name = old.Name
						changes = append(changes, Change{Op: Update, Stage: 2, ZoneID: dz.ID, Zone: dz.Name, Old: &old, New: &nw})
						w, h = append(w[:i], w[i+1:]...), append(h[:j], h[j+1:]...)
						i--
						break
					}
				}
			}
		}
		pair(func(a, b cloudflare.DNSRecord) bool { return normContent(a) == normContent(b) })
		pair(func(a, b cloudflare.DNSRecord) bool { return true })
		for i := range w {
			nw := w[i]
			createNames[k.name] = true
			changes = append(changes, Change{Op: Create, Stage: 2, ZoneID: dz.ID, Zone: dz.Name, New: &nw})
		}
		for i := range h {
			old := h[i]
			changes = append(changes, Change{Op: Delete, Stage: 3, ZoneID: dz.ID, Zone: dz.Name, Old: &old})
		}
	}
	// A record being replaced by one of another type (A → CNAME) must go
	// first: Cloudflare refuses a CNAME next to other records.
	for i := range changes {
		if changes[i].Op == Delete && createNames[strings.ToLower(changes[i].Old.Name)] {
			changes[i].Stage = 2
			c := changes[i]
			copy(changes[1:i+1], changes[0:i])
			changes[0] = c
		}
	}
	return changes
}

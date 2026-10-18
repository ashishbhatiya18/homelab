package cloud

import (
	"fmt"
	"sort"
)

// Export turns the live state into files. prev (the files on disk, may be
// nil) supplies what only the files know: zones' ignore lists, and where a
// tunnel's token lives (or runs) when the nodes don't show it. Notes describe
// what didn't map cleanly (records pointing at tunnels without a matching
// route, routes without DNS, connectors that could not be located).
func Export(st *State, prev *Files) (fs Files, notes []string) {
	byID := map[string]*TunnelState{}
	routed := map[string]string{} // hostname -> tunnel ID
	for i := range st.Tunnels {
		t := &st.Tunnels[i]
		byID[t.ID] = t
		f := Compact(t.Name, t.Config)
		f.ID = t.ID
		f.Local = !t.Remote()
		var pf *TunnelFile
		if prev != nil {
			pf = prev.Tunnel(t.Name)
		}
		switch rs := st.Runners[t.ID]; len(rs) {
		case 0:
			if pf != nil {
				f.Node, f.Stack, f.Container = pf.Node, pf.Stack, pf.Container
			}
			if f.Node == "" {
				notes = append(notes, fmt.Sprintf("tunnel %s: no cloudflared container on any node runs it", t.Name))
			}
		default:
			f.Node, f.Stack, f.Container, f.Secret = rs[0].Node, rs[0].Stack, rs[0].Container, rs[0].Secret
			if len(rs) > 1 {
				notes = append(notes, fmt.Sprintf("tunnel %s: run by %d containers; recorded %s", t.Name, len(rs), rs[0]))
			}
		}
		if f.Secret == "" && pf != nil {
			f.Secret = pf.Secret
		}
		if f.Local {
			notes = append(notes, fmt.Sprintf("tunnel %s: ingress is in cloudflared's local config file, not managed", t.Name))
		}
		for _, h := range f.Hostnames() {
			routed[h] = t.ID
		}
		fs.Tunnels = append(fs.Tunnels, f)
	}
	if rs := st.Runners[""]; len(rs) > 0 {
		for _, r := range rs {
			notes = append(notes, fmt.Sprintf("%s: cloudflared container whose tunnel token could not be read", r))
		}
	}

	hasDNS := map[string]bool{}
	for _, z := range st.Zones {
		zf := ZoneFile{Zone: z.Name, ID: z.ID}
		if prev != nil {
			if pz := prev.Zone(z.Name); pz != nil {
				zf.Ignore = pz.Ignore
			}
		}
		for _, r := range z.Records {
			hasDNS[r.Name] = true
			if zf.Ignored(RelName(r.Name, z.Name)) {
				continue
			}
			if id, ok := TunnelOfRecord(r); ok {
				if routed[r.Name] == id && r.Proxied {
					continue // implied by the tunnel file
				}
				switch t := byID[id]; {
				case t == nil:
					notes = append(notes, fmt.Sprintf("%s: CNAME to a tunnel that no longer exists (%s)", r.Name, id))
				case !r.Proxied:
					notes = append(notes, fmt.Sprintf("%s: tunnel CNAME is not proxied, so it doesn't work", r.Name))
				default:
					notes = append(notes, fmt.Sprintf("%s: CNAME to tunnel %s, which has no route for it (Cloudflare answers with the catch-all)", r.Name, t.Name))
				}
			}
			rec := Record{Name: RelName(r.Name, z.Name), Type: r.Type, Content: r.Content, Proxied: r.Proxied,
				Priority: r.Priority, Comment: r.Comment, ReadOnly: r.ReadOnly()}
			if r.TTL != 1 {
				rec.TTL = r.TTL
			}
			zf.Records = append(zf.Records, rec)
		}
		SortRecords(zf.Records)
		fs.Zones = append(fs.Zones, zf)
	}
	if st.Zones != nil {
		var hosts []string
		for h := range routed {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		for _, h := range hosts {
			if !hasDNS[h] && st.ZoneFor(h) != nil {
				notes = append(notes, fmt.Sprintf("%s: routed by tunnel %s but has no DNS record", h, byID[routed[h]].Name))
			}
		}
	}
	return fs, notes
}

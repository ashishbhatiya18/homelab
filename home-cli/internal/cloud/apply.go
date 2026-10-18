package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ashishbhatiya18/home/internal/cloudflare"
)

// Snapshot is the live state of everything a plan touches, saved before an
// apply so `home cloud undo` can put it back.
type Snapshot struct {
	Time    time.Time        `json:"time"`
	Tunnels []SnapshotTunnel `json:"tunnels"`
	Zones   []SnapshotZone   `json:"zones"`
}

type SnapshotTunnel struct {
	ID     string                  `json:"id"`
	Name   string                  `json:"name"`
	Config cloudflare.TunnelConfig `json:"config"`
}

type SnapshotZone struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Records []cloudflare.DNSRecord `json:"records"`
}

// TakeSnapshot records the live config of every tunnel and zone in changes.
func TakeSnapshot(st *State, changes []Change) Snapshot {
	s := Snapshot{Time: time.Now().UTC()}
	seenT, seenZ := map[string]bool{}, map[string]bool{}
	for _, c := range changes {
		if c.Config != nil && !seenT[c.TunnelID] {
			seenT[c.TunnelID] = true
			if t := st.Tunnel(c.TunnelID); t != nil {
				var cfg cloudflare.TunnelConfig
				if t.Config != nil {
					cfg = *t.Config
				}
				s.Tunnels = append(s.Tunnels, SnapshotTunnel{ID: t.ID, Name: t.Name, Config: cfg})
			}
		}
		if c.ZoneID != "" && !seenZ[c.ZoneID] {
			seenZ[c.ZoneID] = true
			for _, z := range st.Zones {
				if z.ID == c.ZoneID {
					s.Zones = append(s.Zones, SnapshotZone{ID: z.ID, Name: z.Name, Records: z.Records})
				}
			}
		}
	}
	return s
}

func SnapshotDir(stateDir string) string { return filepath.Join(stateDir, "cloud") }

func SaveSnapshot(stateDir string, s Snapshot) (string, error) {
	dir := SnapshotDir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, s.Time.Format("20060102T150405Z")+".json")
	return p, os.WriteFile(p, b, 0o600)
}

// Snapshots lists saved snapshots, newest first.
func Snapshots(stateDir string) []string {
	ps, _ := filepath.Glob(filepath.Join(SnapshotDir(stateDir), "*.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(ps)))
	return ps
}

func LoadSnapshot(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	return &s, json.Unmarshal(b, &s)
}

// Desired turns a snapshot back into a target state: its tunnels' configs and
// its zones' records, managed whole (except what ignore patterns exclude).
func (s *Snapshot) Desired(ignore func(zone string) []string) *Desired {
	var d Desired
	for _, t := range s.Tunnels {
		if len(t.Config.Ingress) == 0 {
			continue // never configured: nothing to restore
		}
		d.Tunnels = append(d.Tunnels, DesiredTunnel{ID: t.ID, Name: t.Name, Config: t.Config})
	}
	for _, z := range s.Zones {
		dz := DesiredZone{ID: z.ID, Name: z.Name, Full: true, Ignore: ignore(z.Name)}
		for _, r := range z.Records {
			if dz.manages(r) {
				dz.Records = append(dz.Records, r)
			}
		}
		d.Zones = append(d.Zones, dz)
	}
	return &d
}

// Apply runs the changes in order and stops at the first error.
func Apply(ctx context.Context, api *cloudflare.Client, accountID string, changes []Change, logf func(string, ...any)) error {
	for _, c := range changes {
		var err error
		switch {
		case c.Config != nil:
			err = api.PutTunnelConfig(ctx, accountID, c.TunnelID, *c.Config)
		case c.Op == Create:
			_, err = api.CreateDNSRecord(ctx, c.ZoneID, *c.New)
		case c.Op == Update:
			err = api.UpdateDNSRecord(ctx, c.ZoneID, c.Old.ID, *c.New)
		case c.Op == Delete:
			err = api.DeleteDNSRecord(ctx, c.ZoneID, c.Old.ID)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", firstLine(c.String()), err)
		}
		logf("%s", firstLine(c.String()))
	}
	return nil
}

func firstLine(s string) string {
	for i, ch := range s {
		if ch == '\n' {
			return s[:i]
		}
	}
	return s
}

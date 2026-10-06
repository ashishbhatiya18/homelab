// Package check builds the node health report used by `home check` and the
// daily notification.
package check

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/node"
	"github.com/ashishbhatiya18/home/internal/notify"
	"github.com/ashishbhatiya18/home/internal/registry"
	"github.com/ashishbhatiya18/home/internal/remote"
	"github.com/ashishbhatiya18/home/internal/stack"
)

type NodeReport struct {
	Node      string
	Err       error
	Status    *node.Status
	Updates   map[string][]stack.Update // stack -> newer images
	CheckErrs []string                  // update checks that could not run
}

// Items returns the things needing attention on this node.
func (r NodeReport) Items(diskWarn int) []string {
	if r.Err != nil {
		return []string{"unreachable: " + r.Err.Error()}
	}
	s := r.Status
	var it []string
	if s.RebootRequired {
		it = append(it, "reboot required")
	}
	if s.AptUpgradable > 0 {
		it = append(it, fmt.Sprintf("%d apt updates", s.AptUpgradable))
	}
	if s.DietPiUpdate != "" {
		it = append(it, "DietPi "+s.DietPiUpdate+" available")
	}
	if len(r.Updates) > 0 {
		var names []string
		for st := range r.Updates {
			names = append(names, st)
		}
		sort.Strings(names)
		it = append(it, fmt.Sprintf("stack updates: %s", strings.Join(names, ", ")))
	}
	if s.DiskPercent >= diskWarn {
		it = append(it, fmt.Sprintf("disk %d%%", s.DiskPercent))
	}
	if p := s.Problems(); len(p) > 0 {
		it = append(it, "unhealthy: "+strings.Join(p, ", "))
	}
	if s.GitopsAgent != "" && s.GitopsAgent != "active" {
		it = append(it, "gitops-agent "+s.GitopsAgent)
	}
	return it
}

// Run gathers a report for every node. refresh updates package lists first
// (needed for accurate apt/DietPi counts); stacks enables image checks.
func Run(ctx context.Context, cfg *homecfg.Config, r *remote.Runner, refresh, stacks bool) []NodeReport {
	reports := make([]NodeReport, len(cfg.Nodes))
	var wg sync.WaitGroup
	for i, n := range cfg.Nodes {
		wg.Add(1)
		go func(i int, n homecfg.Node) {
			defer wg.Done()
			rep := NodeReport{Node: n.Name, Updates: map[string][]stack.Update{}}
			c := &node.Client{R: r, Name: n.Name}
			if !r.Reachable(ctx, n.Name) {
				rep.Err = fmt.Errorf("cannot reach %s over SSH", n.SSH)
				reports[i] = rep
				return
			}
			if refresh {
				if err := c.Refresh(ctx); err != nil {
					rep.CheckErrs = append(rep.CheckErrs, "refresh: "+err.Error())
				}
			}
			st, err := c.Status(ctx)
			if err != nil {
				rep.Err = err
				reports[i] = rep
				return
			}
			rep.Status = st
			if stacks {
				m := &stack.Manager{R: r, Logf: func(string, ...any) {}, Creds: Creds}
				list, err := m.List(ctx, n.Name, n.StacksDir, st)
				if err != nil {
					rep.CheckErrs = append(rep.CheckErrs, "stacks: "+err.Error())
				}
				for name, ups := range m.CheckUpdates(ctx, list) {
					for _, u := range ups {
						if u.Err != nil {
							rep.CheckErrs = append(rep.CheckErrs, fmt.Sprintf("%s: %v", u.Image, u.Err))
							continue
						}
						rep.Updates[name] = append(rep.Updates[name], u)
					}
				}
			}
			reports[i] = rep
		}(i, n)
	}
	wg.Wait()
	return reports
}

// Creds looks up a saved registry login (see `home registry login`).
var Creds func(host string) *registry.Cred

// Summary is one line per node, for notifications.
func Summary(reports []NodeReport, diskWarn int) (string, bool) {
	var lines []string
	attention := false
	for _, r := range reports {
		it := r.Items(diskWarn)
		if len(it) == 0 {
			lines = append(lines, r.Node+": ✓")
			continue
		}
		attention = true
		lines = append(lines, r.Node+": "+strings.Join(it, " · "))
	}
	return strings.Join(lines, "\n"), attention
}

// ---- scheduled daily check ----------------------------------------------------------

type state struct {
	LastCheck time.Time `json:"last_check"`
}

func statePath() string { return filepath.Join(homecfg.StateDir(), "check.json") }

// Daily runs the check once per day after checks.daily_at and notifies.
func Daily(ctx context.Context, cfg *homecfg.Config, r *remote.Runner, logf func(string, ...any)) error {
	var st state
	if b, err := os.ReadFile(statePath()); err == nil {
		json.Unmarshal(b, &st)
	}
	now := time.Now()
	if !st.LastCheck.Before(cfg.Checks.DueSince(now)) {
		return nil
	}
	reports := Run(ctx, cfg, r, true, true)
	summary, attention := Summary(reports, cfg.Checks.DiskWarnPercent)
	logf("daily check:\n%s", summary)
	allDown := true
	for _, rep := range reports {
		if rep.Err == nil {
			allDown = false
		}
	}
	if allDown {
		// Probably away from the home network: try again next run, quietly
		// unless this has been going on for over a day.
		if now.Sub(st.LastCheck) > 36*time.Hour && !st.LastCheck.IsZero() {
			notify.Send("home: nodes unreachable", "No node has been reachable for over a day. Away from home?")
		}
		return nil
	}
	switch {
	case attention:
		notify.Send("home: nodes need attention", summary)
	case cfg.Checks.WhenClear():
		notify.Send("home: all nodes healthy ✓", "Nothing to update, no reboots pending.")
	}
	st.LastCheck = now
	if err := os.MkdirAll(filepath.Dir(statePath()), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(st)
	return os.WriteFile(statePath(), b, 0o600)
}

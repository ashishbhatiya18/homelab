// Package stack manages Docker Compose stacks on a node: one directory per
// stack under the node's stacks_dir, each holding compose.yaml.
//
// Updates are safe by construction: before new images are applied, the
// current image IDs are recorded and tagged on the node so they survive
// pruning; if the stack is not healthy afterwards it is rolled back
// automatically, and `home stack rollback` can return to them later.
package stack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ashishbhatiya18/home/internal/node"
	"github.com/ashishbhatiya18/home/internal/registry"
	"github.com/ashishbhatiya18/home/internal/remote"
)

type Stack struct {
	Node, Name, Dir string
	Containers      []node.Container
}

func (s Stack) ID() string { return s.Node + "/" + s.Name }

func (s Stack) Health() string {
	if len(s.Containers) == 0 {
		return "not running"
	}
	bad := 0
	for _, c := range s.Containers {
		if !c.Healthy() {
			bad++
		}
	}
	if bad == 0 {
		return "healthy"
	}
	return fmt.Sprintf("%d/%d unhealthy", bad, len(s.Containers))
}

type Manager struct {
	R        *remote.Runner
	StateDir string
	Logf     func(string, ...any)
	// Creds returns a login for a registry host (private images), or nil.
	Creds func(host string) *registry.Cred
}

func composeFile(dir, name string) string { return dir + "/" + name + "/compose.yaml" }

// nodeScript is the stacks dir's node.sh. When a node has one, every stack
// operation goes through it (`node.sh compose <stack> …` for plain compose
// commands); without it, compose is called directly.
func nodeScript(dir string) string { return dir + "/node.sh" }

// ComposeCmd is the remote command running `docker compose <args>` for one
// stack, via node.sh when the node has it.
func ComposeCmd(dir, name string, args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = remote.Quote(a)
	}
	a := strings.Join(q, " ")
	return fmt.Sprintf("if [ -f %[1]s ]; then exec bash %[1]s compose %[2]s %[3]s; else exec docker compose -f %[4]s %[3]s; fi",
		remote.Quote(nodeScript(dir)), remote.Quote(name), a, remote.Quote(composeFile(dir, name)))
}

func (m *Manager) compose(ctx context.Context, n, dir, name string, out io.Writer, args ...string) error {
	return m.R.Run(ctx, n, ComposeCmd(dir, name, args...), nil, out)
}

// List returns the node's stacks with their containers.
func (m *Manager) List(ctx context.Context, n string, dir string, st *node.Status) ([]Stack, error) {
	out, err := m.R.Output(ctx, n, fmt.Sprintf(`if [ -f %[2]s ]; then bash %[2]s list; else for d in %[1]s/*/; do [ -f "$d/compose.yaml" ] && basename "$d"; done; fi`, remote.Quote(dir), remote.Quote(nodeScript(dir))))
	if err != nil {
		return nil, err
	}
	var stacks []Stack
	for _, name := range strings.Fields(out) {
		s := Stack{Node: n, Name: name, Dir: dir}
		for _, c := range st.Containers {
			if c.Project == name {
				s.Containers = append(s.Containers, c)
			}
		}
		stacks = append(stacks, s)
	}
	return stacks, nil
}

// lifecycle runs `node.sh <action> <stack>` when the stacks dir has one (it
// knows the node's networks), and the plain compose command otherwise.
func (m *Manager) lifecycle(ctx context.Context, s Stack, action string, out io.Writer, composeArgs ...string) error {
	q := make([]string, len(composeArgs))
	for i, a := range composeArgs {
		q[i] = remote.Quote(a)
	}
	cmd := fmt.Sprintf("if [ -f %[1]s ]; then bash %[1]s %[2]s %[3]s; else docker compose -f %[4]s %[5]s; fi",
		remote.Quote(nodeScript(s.Dir)), action, remote.Quote(s.Name), remote.Quote(composeFile(s.Dir, s.Name)), strings.Join(q, " "))
	return m.R.Run(ctx, s.Node, cmd, nil, out)
}

func (m *Manager) Restart(ctx context.Context, s Stack, svc []string, out io.Writer) error {
	if len(svc) > 0 {
		return m.compose(ctx, s.Node, s.Dir, s.Name, out, append([]string{"restart"}, svc...)...)
	}
	return m.lifecycle(ctx, s, "restart", out, "restart")
}

func (m *Manager) Stop(ctx context.Context, s Stack, out io.Writer) error {
	return m.lifecycle(ctx, s, "stop", out, "stop")
}

func (m *Manager) Start(ctx context.Context, s Stack, out io.Writer) error {
	return m.lifecycle(ctx, s, "start", out, "up", "-d", "--remove-orphans")
}

func (m *Manager) Logs(ctx context.Context, s Stack, follow bool, tail int, svc []string, out io.Writer) error {
	args := []string{"logs", "--tail", fmt.Sprint(tail)}
	if follow {
		args = append(args, "-f")
	}
	return m.compose(ctx, s.Node, s.Dir, s.Name, out, append(args, svc...)...)
}

// ---- update availability ------------------------------------------------------

// Update describes a newer image available for one container.
type Update struct {
	Container, Image string
	Err              error // set when the check itself failed (e.g. private image)
}

// CheckUpdates checks every stack on one node: local digests come from a
// single SSH call, registry digests are fetched in parallel. Nothing is pulled.
func (m *Manager) CheckUpdates(ctx context.Context, stacks []Stack) map[string][]Update {
	res := map[string][]Update{}
	if len(stacks) == 0 {
		return res
	}
	images := map[string]bool{}
	for _, s := range stacks {
		for _, c := range s.Containers {
			images[c.Image] = true
		}
	}
	if len(images) == 0 {
		return res
	}
	var list []string
	for i := range images {
		list = append(list, i)
	}
	sort.Strings(list)
	q := make([]string, len(list))
	for i, img := range list {
		q[i] = remote.Quote(img)
	}
	out, err := m.R.Output(ctx, stacks[0].Node, "for i in "+strings.Join(q, " ")+`; do printf '%s\t' "$i"; docker image inspect --format '{{json .RepoDigests}}' "$i" 2>/dev/null || echo '[]'; done`)
	if err != nil {
		for _, s := range stacks {
			res[s.Name] = append(res[s.Name], Update{Image: "*", Err: err})
		}
		return res
	}
	local := map[string]map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		img, js, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var rds []string
		json.Unmarshal([]byte(js), &rds)
		d := map[string]bool{}
		for _, rd := range rds {
			if _, digest, ok := strings.Cut(rd, "@"); ok {
				d[digest] = true
			}
		}
		local[img] = d
	}

	type result struct {
		newer bool
		err   error
	}
	results := map[string]result{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, img := range list {
		ref, err := registry.Parse(img)
		if err != nil || len(local[img]) == 0 {
			continue // pinned by digest, or built locally
		}
		wg.Add(1)
		go func(img string, ref registry.Ref) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var cred *registry.Cred
			if m.Creds != nil {
				cred = m.Creds(ref.Registry)
			}
			d, err := registry.Digest(ctx, ref, cred)
			mu.Lock()
			results[img] = result{newer: err == nil && !local[img][d], err: err}
			mu.Unlock()
		}(img, ref)
	}
	wg.Wait()
	for _, s := range stacks {
		seen := map[string]bool{}
		for _, c := range s.Containers {
			r, ok := results[c.Image]
			if !ok || seen[c.Image] {
				continue
			}
			seen[c.Image] = true
			if r.err != nil || r.newer {
				res[s.Name] = append(res[s.Name], Update{Container: c.Name, Image: c.Image, Err: r.err})
			}
		}
	}
	return res
}

// ---- update and rollback --------------------------------------------------------

// Snapshot records which image each service ran, for rollback.
type Snapshot struct {
	Stack   string            `json:"stack"`
	Taken   time.Time         `json:"taken"`
	Images  map[string]string `json:"images"`  // image ref -> image ID before the update
	Changed []string          `json:"changed"` // refs whose ID changed
	Tags    map[string]string `json:"tags"`    // image ID -> protective tag on the node
}

func (m *Manager) imageIDs(ctx context.Context, s Stack) (map[string]string, error) {
	var out strings.Builder
	if err := m.compose(ctx, s.Node, s.Dir, s.Name, &out, "config", "--images"); err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, img := range strings.Fields(out.String()) {
		id, err := m.R.Output(ctx, s.Node, "docker image inspect --format '{{.Id}}' "+remote.Quote(img)+" 2>/dev/null || true")
		if err == nil && strings.TrimSpace(id) != "" {
			ids[img] = strings.TrimSpace(id)
		}
	}
	return ids, nil
}

// Update pulls new images, applies them and verifies health, rolling back
// automatically on failure. hook runs after the pull and before anything
// changes (e.g. a database backup). Returns the changed images.
func (m *Manager) Update(ctx context.Context, s Stack, hook func() error, out io.Writer) ([]string, error) {
	before, err := m.imageIDs(ctx, s)
	if err != nil {
		return nil, err
	}
	m.Logf("%s: pulling images", s.ID())
	if err := m.compose(ctx, s.Node, s.Dir, s.Name, out, "pull", "--ignore-buildable", "-q"); err != nil {
		return nil, fmt.Errorf("pull: %w", err)
	}
	after, err := m.imageIDs(ctx, s)
	if err != nil {
		return nil, err
	}
	snap := Snapshot{Stack: s.ID(), Taken: time.Now(), Images: before, Tags: map[string]string{}}
	for ref, id := range after {
		if before[ref] != "" && before[ref] != id {
			snap.Changed = append(snap.Changed, ref)
		}
	}
	sort.Strings(snap.Changed)
	if len(snap.Changed) == 0 {
		m.Logf("%s: already up to date", s.ID())
		return nil, nil
	}
	// Keep the old images from being pruned so rollback stays possible.
	stamp := snap.Taken.Format("20060102-150405")
	for i, ref := range snap.Changed {
		tag := fmt.Sprintf("home-rollback/%s:%s-%d", s.Name, stamp, i)
		if err := m.R.Run(ctx, s.Node, "docker tag "+remote.Quote(before[ref])+" "+remote.Quote(tag), nil, nil); err != nil {
			return nil, fmt.Errorf("protecting old image: %w", err)
		}
		snap.Tags[before[ref]] = tag
	}
	if err := m.saveSnapshot(snap); err != nil {
		return nil, err
	}
	if hook != nil {
		if err := hook(); err != nil {
			return nil, fmt.Errorf("pre-update hook failed, nothing changed: %w", err)
		}
	}
	m.Logf("%s: applying %d new image(s): %s", s.ID(), len(snap.Changed), strings.Join(snap.Changed, ", "))
	if err := m.compose(ctx, s.Node, s.Dir, s.Name, out, "up", "-d", "--remove-orphans"); err != nil {
		return snap.Changed, m.rollbackAfter(ctx, s, snap, fmt.Errorf("up: %w", err), out)
	}
	if err := m.WaitHealthy(ctx, s); err != nil {
		return snap.Changed, m.rollbackAfter(ctx, s, snap, err, out)
	}
	m.Logf("%s: updated and healthy", s.ID())
	return snap.Changed, nil
}

func (m *Manager) rollbackAfter(ctx context.Context, s Stack, snap Snapshot, cause error, out io.Writer) error {
	m.Logf("%s: update failed (%v); rolling back", s.ID(), cause)
	if err := m.applySnapshot(ctx, s, snap, out); err != nil {
		return fmt.Errorf("update failed (%v) AND rollback failed: %w", cause, err)
	}
	return fmt.Errorf("update failed and was rolled back: %w", cause)
}

// Rollback returns a stack to the images it ran before its last update.
func (m *Manager) Rollback(ctx context.Context, s Stack, out io.Writer) error {
	snap, err := m.lastSnapshot(s.ID())
	if err != nil {
		return err
	}
	m.Logf("%s: rolling back to images from %s", s.ID(), snap.Taken.Format("2006-01-02 15:04"))
	return m.applySnapshot(ctx, s, snap, out)
}

func (m *Manager) applySnapshot(ctx context.Context, s Stack, snap Snapshot, out io.Writer) error {
	for _, ref := range snap.Changed {
		if err := m.R.Run(ctx, s.Node, "docker tag "+remote.Quote(snap.Images[ref])+" "+remote.Quote(ref), nil, nil); err != nil {
			return fmt.Errorf("restore image %s: %w", ref, err)
		}
	}
	if err := m.compose(ctx, s.Node, s.Dir, s.Name, out, "up", "-d", "--remove-orphans"); err != nil {
		return err
	}
	return m.WaitHealthy(ctx, s)
}

// WaitHealthy waits until every container of the stack is running, not
// restarting and (if it has a health check) healthy, and stays so.
func (m *Manager) WaitHealthy(ctx context.Context, s Stack) error {
	deadline := time.Now().Add(3 * time.Minute)
	stable := 0
	for {
		var out strings.Builder
		err := m.compose(ctx, s.Node, s.Dir, s.Name, &out, "ps", "-a", "--format", "{{.Name}}|{{.State}}|{{.Status}}")
		ok, why := err == nil, ""
		if err != nil {
			why = err.Error()
		}
		var lines []string
		for _, l := range strings.Split(out.String(), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
		if ok && len(lines) == 0 {
			ok, why = false, "no containers"
		}
		for _, l := range lines {
			f := strings.SplitN(l, "|", 3)
			if len(f) < 3 {
				continue
			}
			st := f[2]
			if f[1] != "running" || strings.Contains(st, "unhealthy") || strings.Contains(st, "health: starting") || strings.Contains(st, "Restarting") {
				ok, why = false, f[0]+": "+st
			}
		}
		if ok {
			stable++
			if stable >= 3 { // healthy across ~15s, not just a lucky moment
				return nil
			}
		} else {
			stable = 0
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy after 3 minutes (%s)", why)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func (m *Manager) snapDir(id string) string {
	return filepath.Join(m.StateDir, "rollback", strings.ReplaceAll(id, "/", "_"))
}

func (m *Manager) saveSnapshot(s Snapshot) error {
	dir := m.snapDir(s.Stack)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(dir, s.Taken.Format("20060102-150405")+".json"), b, 0o600)
}

func (m *Manager) lastSnapshot(id string) (Snapshot, error) {
	files, _ := filepath.Glob(filepath.Join(m.snapDir(id), "*.json"))
	if len(files) == 0 {
		return Snapshot{}, errors.New("no previous update recorded for " + id)
	}
	sort.Strings(files)
	b, err := os.ReadFile(files[len(files)-1])
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	return s, json.Unmarshal(b, &s)
}

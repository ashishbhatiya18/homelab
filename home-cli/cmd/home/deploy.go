package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ashishbhatiya18/home/internal/deploy"
	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/node"
	"github.com/ashishbhatiya18/home/internal/notify"
)

func (a *app) deployer(ctx context.Context, out io.Writer, logf func(string, ...any)) *deploy.Deployer {
	return &deploy.Deployer{R: a.r, SM: a.sm, Creds: registryCreds, Logf: logf, Out: out,
		Hook: func(id string) func() error { return a.hook(ctx, id) }}
}

// bundleNodes returns the target nodes (in upgrade order) that have a bundle.
func (a *app) bundleNodes(target string) ([]homecfg.Node, error) {
	nodes, err := a.cfg.Ordered(target)
	if err != nil {
		return nil, err
	}
	var out []homecfg.Node
	for _, n := range nodes {
		if n.Bundle != "" {
			out = append(out, n)
		} else if target != "" && target != "all" {
			return nil, fmt.Errorf("%s has no bundle configured (nodes[].bundle in %s)", n.Name, homecfg.DefaultPath())
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no node has a bundle configured (nodes[].bundle)")
	}
	return out, nil
}

// deployLock keeps a manual `home deploy` and the background job apart.
func deployLock() (func(), error) {
	p := filepath.Join(homecfg.StateDir(), "deploy.lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another deploy is running (manual or the background job); try again in a minute")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func (a *app) deployCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	dry := fs.Bool("dry-run", false, "show what would change, change nothing")
	tag := fs.String("tag", "latest", "bundle tag to deploy, e.g. a commit sha")
	back := fs.Bool("rollback", false, "go back to the release deployed before the current one")
	force := fs.Bool("force", false, "re-apply even when the release is already deployed")
	recreate := fs.Bool("recreate", false, "start every stack with fresh containers (implies --force)")
	fs.Parse(reorder(args))
	target := "all"
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}
	nodes, err := a.bundleNodes(target)
	if err != nil {
		return err
	}
	unlock, err := deployLock()
	if err != nil {
		return err
	}
	defer unlock()

	d := a.deployer(ctx, os.Stdout, logf)
	var plans []*deploy.Plan
	for _, n := range nodes {
		cur, err := d.Current(ctx, n)
		if err != nil {
			return err
		}
		var digest string
		if *back {
			if cur.Previous == "" {
				return fmt.Errorf("%s: no previous release recorded", n.Name)
			}
			digest = cur.Previous
		} else if digest, err = d.Latest(ctx, n, *tag); err != nil {
			return fmt.Errorf("%s: %w", n.Name, err)
		}
		if digest == cur.Digest && !*force && !*recreate {
			fmt.Printf("%s: %s is deployed and current\n", n.Name, cur.Short())
			continue
		}
		logf("%s: preparing release", n.Name)
		p, err := d.Prepare(ctx, n, digest, *recreate)
		if err != nil {
			return err
		}
		printPlan(p)
		plans = append(plans, p)
	}
	if len(plans) == 0 {
		return nil
	}
	if *dry {
		fmt.Println("\nDry run: nothing changed (the releases are extracted on the nodes, not deployed).")
		return nil
	}
	if err := confirm(*yes, "\nDeploy?"); err != nil {
		return err
	}
	var done []string
	for _, p := range plans {
		if err := d.Apply(ctx, p); err != nil {
			notify.Send("home: deploy to "+p.Node.Name+" failed", err.Error())
			return err
		}
		done = append(done, fmt.Sprintf("%s: %s", p.Node.Name, p.To.Short()))
	}
	notify.Send("home: deployed ✓", strings.Join(done, "\n"))
	return nil
}

func printPlan(p *deploy.Plan) {
	fmt.Printf("\n%s: %s → %s\n", p.Node.Name, p.From.Short(), p.To.Short())
	if p.From.Digest == "" {
		fmt.Println("  first deploy: every stack is started from the bundle; containers whose")
		fmt.Println("  bind-mounted files moved are recreated once")
	}
	switch {
	case p.Recreate && len(p.Changed) > 0:
		fmt.Println("  recreate (in order): " + strings.Join(p.Changed, ", "))
	case len(p.Changed) > 0:
		fmt.Println("  start (in order): " + strings.Join(p.Changed, ", "))
	}
	if len(p.Files) > 0 {
		fmt.Println("  node files:       " + strings.Join(p.Files, ", "))
	}
	for _, s := range p.Removed {
		fmt.Printf("  removed:          %s (keeps running; `node.sh down %s` on the node removes it)\n", s, s)
	}
	if p.Empty() {
		fmt.Println("  no stack changes (only records the new release)")
	}
}

// ---- background jobs ------------------------------------------------------------

type jobState struct {
	Last   time.Time `json:"last"`
	Result string    `json:"result,omitempty"`
}

func jobsPath() string { return filepath.Join(homecfg.StateDir(), "jobs.json") }

func loadJobs() map[string]jobState {
	m := map[string]jobState{}
	if b, err := os.ReadFile(jobsPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func saveJob(name, result string) {
	m := loadJobs()
	m[name] = jobState{Last: time.Now(), Result: result}
	b, _ := json.MarshalIndent(m, "", "  ")
	os.MkdirAll(filepath.Dir(jobsPath()), 0o700)
	os.WriteFile(jobsPath(), b, 0o600)
}

// deployJob rolls out new bundles to nodes that already run one. A node's
// first deploy is always manual (`home deploy <node>`).
func (a *app) deployJob(ctx context.Context) error {
	if !a.cfg.Jobs.DeployOn() {
		return nil
	}
	nodes, err := a.bundleNodes("all")
	if err != nil {
		return nil // no bundles configured: nothing to do
	}
	unlock, err := deployLock()
	if err != nil {
		log.Printf("deploy: %v", err)
		return nil
	}
	defer unlock()
	d := a.deployer(ctx, io.Discard, log.Printf)
	var results, errs []string
	for _, n := range nodes {
		if !a.r.Reachable(ctx, n.Name) {
			continue // away from home: next run
		}
		cur, err := d.Current(ctx, n)
		if err != nil || cur.Digest == "" {
			continue
		}
		latest, err := d.Latest(ctx, n, "latest")
		if err != nil {
			log.Printf("deploy: %s: %v", n.Name, err)
			continue
		}
		if latest == cur.Digest {
			continue
		}
		p, err := d.Prepare(ctx, n, latest, false)
		if err == nil {
			err = d.Apply(ctx, p)
		}
		if err != nil {
			notify.Send("home: deploy to "+n.Name+" failed", err.Error())
			errs = append(errs, n.Name+": "+err.Error())
			continue
		}
		msg := fmt.Sprintf("%s: %s → %s", n.Name, p.From.Short(), p.To.Short())
		if len(p.Changed) > 0 {
			msg += " (" + strings.Join(p.Changed, ", ") + ")"
		}
		notify.Send("home: deployed ✓", msg)
		results = append(results, msg)
	}
	switch {
	case len(errs) > 0:
		saveJob("deploy", "failed: "+strings.Join(errs, "; "))
		return errors.New(strings.Join(errs, "; "))
	case len(results) > 0:
		saveJob("deploy", strings.Join(results, "; "))
	default:
		saveJob("deploy", "up to date")
	}
	return nil
}

// cleanupJob reclaims unused images and caches once a day after
// checks.daily_at, only with the cleanups marked safe to run unattended.
func (a *app) cleanupJob(ctx context.Context, force bool) error {
	if !a.cfg.Jobs.CleanupOn() && !force {
		return nil
	}
	if !force && !loadJobs()["cleanup"].Last.Before(a.cfg.Checks.DueSince(time.Now())) {
		return nil
	}
	var results []string
	reached := false
	for _, n := range a.cfg.Nodes {
		if !a.r.Reachable(ctx, n.Name) {
			continue
		}
		reached = true
		c := &node.Client{R: a.r, Name: n.Name}
		rep, err := c.Disk(ctx, 30)
		if err != nil {
			results = append(results, n.Name+": "+err.Error())
			continue
		}
		var chosen []node.Cleanup
		for _, cl := range rep.Cleanups {
			if cl.Unattended {
				chosen = append(chosen, cl)
			}
		}
		if len(chosen) == 0 {
			results = append(results, n.Name+": nothing to reclaim")
			continue
		}
		if err := c.Clean(ctx, chosen, io.Discard); err != nil {
			results = append(results, n.Name+": "+err.Error())
			continue
		}
		after, _ := c.Disk(ctx, 30)
		freed := int64(0)
		if after != nil {
			freed = rep.Used - after.Used
		}
		results = append(results, fmt.Sprintf("%s: freed %s", n.Name, node.Human(freed)))
	}
	if !reached {
		return nil // try again next run
	}
	log.Printf("cleanup: %s", strings.Join(results, "; "))
	saveJob("cleanup", strings.Join(results, "; "))
	return nil
}

func (a *app) jobsCmd(ctx context.Context, args []string) error {
	if len(args) == 2 && args[0] == "run" {
		switch args[1] {
		case "deploy":
			return a.deployJob(ctx)
		case "cleanup":
			return a.cleanupJob(ctx, true)
		case "check":
			return run(ctx, "check", nil)
		case "backup":
			return run(ctx, "br", []string{"run"})
		}
		return fmt.Errorf("unknown job %q (backup, check, deploy, cleanup)", args[1])
	}
	if len(args) != 0 {
		return errors.New("usage: home jobs | home jobs run <backup|check|deploy|cleanup>")
	}
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	st := loadJobs()
	last := func(name string) string {
		s, ok := st[name]
		if !ok {
			return "-"
		}
		return s.Last.Format("2006-01-02 15:04") + "  " + s.Result
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "JOB\tSCHEDULE\tSTATE\tLAST RUN")
	fmt.Fprintf(w, "backup\tdaily, as set in br.yaml\ton\tsee `home br status`\n")
	fmt.Fprintf(w, "check\tdaily after %s\ton\t-\n", a.cfg.Checks.DailyAt)
	fmt.Fprintf(w, "deploy\tevery run (15 min)\t%s\t%s\n", onOff(a.cfg.Jobs.DeployOn()), last("deploy"))
	fmt.Fprintf(w, "cleanup\tdaily after %s\t%s\t%s\n", a.cfg.Checks.DailyAt, onOff(a.cfg.Jobs.CleanupOn()), last("cleanup"))
	w.Flush()
	fmt.Println("\nJobs run from the background service (`home install`); switch deploy/cleanup in the jobs: section of config.yaml.")
	return nil
}

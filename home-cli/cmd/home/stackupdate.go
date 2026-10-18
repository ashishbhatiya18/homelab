package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ashishbhatiya18/home/internal/check"
	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/node"
	"github.com/ashishbhatiya18/home/internal/notify"
	"github.com/ashishbhatiya18/home/internal/stack"
)

// updateNodeStacks updates the named stacks of one node one at a time, in
// the node's start order (node.conf's ORDER first). Each update rolls itself
// back on failure. A failing ORDER stack stops the run, since the stacks
// after it depend on it (err is set); a failing app stack is listed in
// failed and the rest go ahead. Over Tailscale, a stack running Tailscale
// itself is skipped: recreating it would cut the connection mid-update.
func (a *app) updateNodeStacks(ctx context.Context, n homecfg.Node, names []string, out io.Writer) (updated, failed, skipped []string, err error) {
	st, err := (&node.Client{R: a.r, Name: n.Name}).Status(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	list, err := a.sm.List(ctx, n.Name, n.StacksDir, st)
	if err != nil {
		return nil, nil, nil, err
	}
	byName := map[string]stack.Stack{}
	for _, s := range list {
		byName[s.Name] = s
	}
	ordered, first, err := a.sm.Ordered(ctx, n.Name, n.StacksDir, names)
	if err != nil {
		return nil, nil, nil, err
	}
	viaTailscale := n.Tailscale != "" && a.r.Target(n.Name) == n.Tailscale
	for _, name := range ordered {
		s, ok := byName[name]
		if !ok {
			failed = append(failed, fmt.Sprintf("%s/%s: no such stack", n.Name, name))
			continue
		}
		if viaTailscale && runsTailscale(s) {
			logf("%s: skipped — it runs Tailscale, which this connection goes through; update it from the LAN", s.ID())
			skipped = append(skipped, s.ID())
			continue
		}
		changed, uerr := a.sm.Update(ctx, s, a.hook(ctx, s.ID()), out)
		if uerr != nil {
			if first[name] {
				return updated, failed, skipped, fmt.Errorf("%s (stacks after it depend on it, stopped): %w", s.ID(), uerr)
			}
			logf("%s: update failed, continuing with the other stacks: %v", s.ID(), uerr)
			failed = append(failed, fmt.Sprintf("%s: %v", s.ID(), uerr))
			continue
		}
		if len(changed) > 0 {
			updated = append(updated, s.ID())
		}
	}
	return updated, failed, skipped, nil
}

func runsTailscale(s stack.Stack) bool {
	for _, c := range s.Containers {
		if c.Name == "tailscale" || strings.Contains(c.Image, "tailscale/tailscale") {
			return true
		}
	}
	return false
}

// updateAllStacks is `home stack update <node|all>`: every stack with a
// newer image, node by node in upgrade order.
func (a *app) updateAllStacks(ctx context.Context, target string, yes, dry bool) error {
	nodes, err := a.cfg.Ordered(target)
	if err != nil {
		return err
	}
	byNode := map[string]check.NodeReport{}
	for _, r := range check.Run(ctx, a.cfg, a.r, false, true) {
		byNode[r.Node] = r
	}
	fmt.Println("Stacks with newer images:")
	plan := map[string][]string{}
	total := 0
	for _, n := range nodes {
		r := byNode[n.Name]
		if r.Err != nil {
			fmt.Printf("  %s: %v\n", n.Name, r.Err)
			continue
		}
		for _, e := range r.CheckErrs {
			fmt.Printf("  %s: ! %s\n", n.Name, e)
		}
		var names []string
		for s := range r.Updates {
			names = append(names, s)
		}
		ordered, _, err := a.sm.Ordered(ctx, n.Name, n.StacksDir, names)
		if err != nil {
			return fmt.Errorf("%s: %w", n.Name, err)
		}
		if len(ordered) == 0 {
			fmt.Printf("  %s: up to date\n", n.Name)
			continue
		}
		for _, s := range ordered {
			var imgs []string
			for _, u := range r.Updates[s] {
				imgs = append(imgs, u.Image)
			}
			sort.Strings(imgs)
			fmt.Printf("  %s/%s: %s\n", n.Name, s, strings.Join(dedupe(imgs), ", "))
		}
		plan[n.Name] = ordered
		total += len(ordered)
	}
	if total == 0 {
		fmt.Println("\nNothing to update.")
		return nil
	}
	if dry {
		fmt.Println("\nDry run: nothing changed.")
		return nil
	}
	if err := confirm(yes, fmt.Sprintf("\nUpdate %d stack(s), one at a time?", total)); err != nil {
		return err
	}
	var updated, failed, skipped []string
	var stopErr error
	for _, n := range nodes {
		if len(plan[n.Name]) == 0 {
			continue
		}
		u, f, sk, err := a.updateNodeStacks(ctx, n, plan[n.Name], io.Discard)
		updated, failed, skipped = append(updated, u...), append(failed, f...), append(skipped, sk...)
		if err != nil {
			stopErr = err
			break
		}
	}
	var lines []string
	if len(updated) > 0 {
		lines = append(lines, "updated: "+strings.Join(updated, ", "))
	}
	if len(skipped) > 0 {
		lines = append(lines, "skipped (run Tailscale; update from the LAN): "+strings.Join(skipped, ", "))
	}
	if len(failed) > 0 {
		lines = append(lines, "rolled back / failed:\n  "+strings.Join(failed, "\n  "))
	}
	if stopErr != nil {
		lines = append(lines, "stopped: "+stopErr.Error())
	}
	summary := strings.Join(lines, "\n")
	if summary == "" {
		summary = "nothing changed"
	}
	if stopErr != nil || len(failed) > 0 {
		fmt.Println("\n✗ Stack updates:\n" + summary)
		notify.Send("home: stack updates had failures", summary)
		return errors.New("some stack updates failed")
	}
	fmt.Println("\n✓ Stack updates:\n" + summary)
	notify.Send("home: stacks updated ✓", summary)
	return nil
}

func dedupe(s []string) []string {
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

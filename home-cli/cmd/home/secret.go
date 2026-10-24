package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ashishbhatiya18/home/internal/deploy"
	"github.com/ashishbhatiya18/home/internal/notify"
	"github.com/ashishbhatiya18/home/internal/remote"
	"github.com/ashishbhatiya18/home/internal/secretfiles"
)

const secretUsage = `usage: home secret migrate <node>/<stack> [--dry-run] [--yes] [--keep-old] [--tag <tag>]`

func (a *app) secretCmd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "migrate" {
		return errors.New(secretUsage)
	}
	return a.secretMigrate(ctx, args[1:])
}

// secretMigrate moves a stack's secret files into the layout its new
// compose.yaml expects, following the secrets.migrate.yaml in the bundle:
// it creates the new files on the node (copies made there, so values never
// cross SSH), deploys the release, and once the stack is healthy deletes the
// old files no deployed stack references any more. A failed deploy rolls
// back to the old compose, which still has its files; the new ones are removed.
func (a *app) secretMigrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("secret migrate", flag.ExitOnError)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	dry := fs.Bool("dry-run", false, "show what would happen, change nothing")
	keep := fs.Bool("keep-old", false, "leave the old files in place")
	tag := fs.String("tag", "latest", "bundle tag to deploy, e.g. a commit sha")
	fs.Parse(reorderFlags(args, "--tag", "-tag"))
	if fs.NArg() != 1 || !strings.Contains(fs.Arg(0), "/") {
		return errors.New(secretUsage)
	}
	nodeName, stackName, _ := strings.Cut(fs.Arg(0), "/")
	n, err := a.cfg.Node(nodeName)
	if err != nil {
		return err
	}
	if n.Bundle == "" {
		return fmt.Errorf("%s has no bundle configured: secret migrations run with a deploy", n.Name)
	}
	unlock, err := deployLock()
	if err != nil {
		return err
	}
	defer unlock()

	d := a.deployer(ctx, os.Stdout, logf)
	cur, err := d.Current(ctx, *n)
	if err != nil {
		return err
	}
	digest, err := d.Latest(ctx, *n, *tag)
	if err != nil {
		return fmt.Errorf("%s: %w", n.Name, err)
	}
	logf("%s: preparing release", n.Name)
	plan, err := d.Prepare(ctx, *n, digest, false)
	if err != nil {
		return err
	}
	file := deploy.ContentDir(*n, digest) + "/" + stackName + "/" + secretfiles.MigrateFile
	raw, err := a.r.Output(ctx, n.Name, "cat "+remote.Quote(file)+" 2>/dev/null || true")
	if err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%s/%s: the release %s has no %s for this stack", n.Name, stackName, plan.To.Short(), secretfiles.MigrateFile)
	}
	moves, err := secretfiles.ParseMoves([]byte(raw))
	if err != nil {
		return fmt.Errorf("%s/%s: %w", n.Name, stackName, err)
	}

	dir, live := deploy.SecretsDir(*n), strings.TrimRight(n.StacksDir, "/")
	run := func(action string, created []string) (secretfiles.Result, error) {
		out, err := a.r.Output(ctx, n.Name, "bash -s <<'HOME_EOF'\n"+secretfiles.Script(action, dir, live, stackName, moves, created)+"HOME_EOF")
		return secretfiles.ParseResult(out), err
	}
	st, err := run("status", nil)
	if err != nil {
		return err
	}
	fmt.Printf("\n%s/%s: secrets in %s\n", n.Name, stackName, dir)
	pending, bad := 0, false
	for i, m := range moves {
		s := st.States[i+1]
		fmt.Printf("  %-9s %s\n", s, m)
		switch {
		case s == "pending":
			pending++
		case s == "missing" || strings.HasPrefix(s, "conflict") || s == "":
			bad = true
		}
	}
	if bad {
		return errors.New("fix the moves marked missing or conflict first (nothing changed)")
	}
	deploying := digest != cur.Digest
	if deploying {
		printPlan(plan)
	} else {
		fmt.Printf("\n%s: %s is deployed already\n", n.Name, cur.Short())
	}
	if *dry {
		fmt.Println("\nDry run: nothing changed.")
		return nil
	}
	if pending > 0 || deploying {
		if err := confirm(*yes, "\nCreate the new files and deploy?"); err != nil {
			return err
		}
	}

	var created []string
	if pending > 0 {
		res, err := run("apply", nil)
		created = res.Created
		if err == nil {
			for i := range moves {
				if s := res.States[i+1]; s != "copied" && s != "done" {
					err = fmt.Errorf("move %d (%s) is %s after copying", i+1, moves[i], s)
				}
			}
		}
		if err != nil {
			return a.undoMigrate(err, run, created)
		}
		logf("%s: created %s", n.Name, strings.Join(created, ", "))
	}
	if deploying {
		// Look again now that the files exist.
		if plan, err = d.Prepare(ctx, *n, digest, false); err == nil {
			err = d.Apply(ctx, plan)
		}
		if err != nil {
			notify.Send("home: secret migration on "+n.Name+" failed", err.Error())
			return a.undoMigrate(err, run, created)
		}
	}

	if *keep {
		logf("old files kept (--keep-old); run again without it to delete them")
		return nil
	}
	if err := confirm(*yes, "Delete the old files no deployed stack uses any more?"); err != nil {
		logf("old files kept; run `home secret migrate %s/%s` again to delete them", n.Name, stackName)
		return nil
	}
	res, err := run("cleanup", nil)
	for _, p := range res.Removed {
		logf("%s: removed %s", n.Name, p)
	}
	for _, k := range res.Kept {
		logf("%s: kept %s", n.Name, k)
	}
	if err != nil {
		return err
	}
	logf("%s/%s: secrets migrated ✓ — %s can go from the repo now", n.Name, stackName, secretfiles.MigrateFile)
	return nil
}

// undoMigrate removes the files a migration created, after cause.
func (a *app) undoMigrate(cause error, run func(string, []string) (secretfiles.Result, error), created []string) error {
	if len(created) == 0 {
		return cause
	}
	if _, err := run("undo", created); err != nil {
		return fmt.Errorf("%w — AND removing the new files failed: %v (remove by hand: %s)", cause, err, strings.Join(created, ", "))
	}
	return fmt.Errorf("%w — the new files were removed; the old ones are untouched", cause)
}

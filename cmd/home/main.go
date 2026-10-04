// Command home manages a small fleet of self-hosted nodes and their Docker
// Compose stacks — status, stack updates with rollback, apt and DietPi
// upgrades, reboots — and, via `home br`, encrypted database backups.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	brcli "github.com/ashishbhatiya18/home/internal/br/cli"
	"github.com/ashishbhatiya18/home/internal/br/secret"
	"github.com/ashishbhatiya18/home/internal/check"
	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/homesetup"
	"github.com/ashishbhatiya18/home/internal/node"
	"github.com/ashishbhatiya18/home/internal/notify"
	"github.com/ashishbhatiya18/home/internal/prompt"
	"github.com/ashishbhatiya18/home/internal/registry"
	"github.com/ashishbhatiya18/home/internal/remote"
	"github.com/ashishbhatiya18/home/internal/service"
	"github.com/ashishbhatiya18/home/internal/stack"
)

var version = "dev"

const usage = `home — run your homelab nodes and stacks from your Mac

Overview:
  home status                       nodes: OS/DietPi, pending updates, reboot, disk, problems
  home stacks [node]                stacks with health and available image updates
  home check                        full check now (refreshes package lists); notifies

Stacks:
  home stack restart <node>/<stack> [service…]
  home stack stop|start <node>/<stack>
  home stack logs <node>/<stack> [service…] [-f] [--tail N]
  home stack update <node>/<stack> [--yes]   pull, apply, verify health; auto-rollback
  home stack rollback <node>/<stack>         back to the images before the last update

Nodes:
  home apt check|upgrade <node|all> [--yes]
  home dietpi check|upgrade <node|all> [--yes]
  home node reboot <node> [--yes]            waits until every container is back
  home upgrade <node|all> [--dry-run] [--yes] [--no-reboot]
                                    apt → DietPi → stack updates → reboot if needed,
                                    node by node in upgrade order

Backups:
  home br …                         encrypted Postgres backups (see ` + "`home br help`" + `)

Setup:
  home registry login <host>        read-only login to check private images (Keychain)
  home setup                        nodes and stacks (runs automatically the first time)
  home install | uninstall          background service: daily backups + daily node check
  home version
`

func init() { check.Creds = registryCreds }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	brcli.Version = version
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if err := run(ctx, cmd, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type app struct {
	cfg *homecfg.Config
	r   *remote.Runner
	sm  *stack.Manager
}

func load() (*app, error) {
	cfg, err := homecfg.Load(homecfg.DefaultPath())
	if err != nil {
		return nil, err
	}
	r := &remote.Runner{Hosts: cfg.Hosts(), ConnectTimeout: 6 * time.Second}
	return &app{cfg: cfg, r: r, sm: &stack.Manager{R: r, StateDir: homecfg.StateDir(), Logf: logf, Creds: registryCreds}}, nil
}

// registryCreds reads a login saved by `home registry login`.
func registryCreds(host string) *registry.Cred {
	v, err := secret.Get("registry:" + host)
	if err != nil {
		return nil
	}
	user, pass, ok := strings.Cut(v, "\n")
	if !ok {
		return nil
	}
	return &registry.Cred{User: user, Password: pass}
}

func registryCmd(args []string) error {
	if len(args) != 2 || (args[0] != "login" && args[0] != "logout") {
		return errors.New("usage: home registry login|logout <host>   (e.g. ghcr.io)")
	}
	host := args[1]
	if args[0] == "logout" {
		return secret.Delete("registry:" + host)
	}
	fmt.Printf("Saving a read-only login for %s in the macOS Keychain. It is only used to check\n", host)
	fmt.Println("for newer images (nothing is pulled). For ghcr.io, use a GitHub token with just")
	fmt.Println("the read:packages scope.")
	user, err := prompt.Ask("Username", "")
	if err != nil {
		return err
	}
	pass, err := prompt.Password("Token / password: ")
	if err != nil {
		return err
	}
	if err := secret.Set("registry:"+host, user+"\n"+pass); err != nil {
		return err
	}
	fmt.Println("✓ saved. `home stacks` will now check private images on", host)
	return nil
}

func logf(f string, a ...any) { fmt.Printf("• "+f+"\n", a...) }

func run(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "br":
		sub := ""
		if len(args) > 0 {
			sub, args = args[0], args[1:]
		}
		return brcli.Run(ctx, sub, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "--version", "-v":
		fmt.Println("home", version)
		return nil
	case "setup":
		return homesetup.Run(ctx, homecfg.DefaultPath())
	case "install":
		if err := service.Install(); err != nil {
			return err
		}
		fmt.Println("✓ home runs in the background: daily backups (home br) and a daily node check.")
		fmt.Println("  Manage it with `brew services info|restart|stop home` or `home uninstall`.")
		return nil
	case "uninstall":
		return service.Uninstall()
	case "run":
		return scheduled(ctx)
	case "registry":
		return registryCmd(args)
	case "":
		if _, err := os.Stat(homecfg.DefaultPath()); errors.Is(err, os.ErrNotExist) {
			return homesetup.Run(ctx, homecfg.DefaultPath())
		}
		cmd = "status"
	}

	a, err := load()
	if err != nil {
		return err
	}
	switch cmd {
	case "status":
		return a.status(ctx)
	case "stacks":
		only := ""
		if len(args) > 0 {
			only = args[0]
		}
		return a.stacks(ctx, only)
	case "check":
		reports := check.Run(ctx, a.cfg, a.r, true, true)
		printReports(reports, a.cfg.Checks.DiskWarnPercent)
		sum, attention := check.Summary(reports, a.cfg.Checks.DiskWarnPercent)
		if attention {
			notify.Send("home: nodes need attention", sum)
		}
		return nil
	case "stack":
		return a.stackCmd(ctx, args)
	case "apt", "dietpi":
		return a.pkgCmd(ctx, cmd, args)
	case "node":
		return a.nodeCmd(ctx, args)
	case "upgrade":
		return a.upgrade(ctx, args)
	}
	return fmt.Errorf("unknown command %q — see `home help`", cmd)
}

// scheduled is the service entry point: daily backups and the daily check.
func scheduled(ctx context.Context) error {
	var errs []string
	if err := brcli.Scheduled(ctx); err != nil {
		errs = append(errs, "backups: "+err.Error())
	}
	if _, err := os.Stat(homecfg.DefaultPath()); err == nil {
		a, err := load()
		if err != nil {
			notify.Send("home: node check cannot run", err.Error())
			errs = append(errs, err.Error())
		} else if err := check.Daily(ctx, a.cfg, a.r, log.Printf); err != nil {
			errs = append(errs, "check: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// ---- overview ---------------------------------------------------------------

func (a *app) status(ctx context.Context) error {
	reports := check.Run(ctx, a.cfg, a.r, false, false)
	printReports(reports, a.cfg.Checks.DiskWarnPercent)
	fmt.Println("\n(package counts are as of the last refresh; `home check` refreshes them and checks images)")
	return nil
}

func printReports(reports []check.NodeReport, diskWarn int) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tDIETPI\tOS\tUPTIME\tAPT\tREBOOT\tDISK\tCONTAINERS\tATTENTION")
	for _, r := range reports {
		if r.Err != nil {
			fmt.Fprintf(w, "%s\t-\t-\t-\t-\t-\t-\t-\t%v\n", r.Node, r.Err)
			continue
		}
		s := r.Status
		dp := s.DietPi
		if s.DietPiUpdate != "" {
			dp += " → " + s.DietPiUpdate
		}
		reboot := "no"
		if s.RebootRequired {
			reboot = "YES"
		}
		running := 0
		for _, c := range s.Containers {
			if c.State == "running" {
				running++
			}
		}
		items := r.Items(diskWarn)
		att := "✓"
		if len(items) > 0 {
			att = strings.Join(items, " · ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%d%%\t%d/%d\t%s\n", r.Node, dp, short(s.OS), human(s.Uptime), s.AptUpgradable, reboot, s.DiskPercent, running, len(s.Containers), att)
	}
	w.Flush()
	for _, r := range reports {
		for _, e := range r.CheckErrs {
			fmt.Printf("  note (%s): %s\n", r.Node, e)
		}
	}
}

func (a *app) stacks(ctx context.Context, only string) error {
	nodes, err := a.cfg.Ordered(only)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "STACK\tCONTAINERS\tHEALTH\tUPDATE")
	for _, n := range nodes {
		c := &node.Client{R: a.r, Name: n.Name}
		st, err := c.Status(ctx)
		if err != nil {
			fmt.Fprintf(w, "%s/*\t-\t%v\t\n", n.Name, err)
			continue
		}
		list, err := a.sm.List(ctx, n.Name, n.StacksDir, st)
		if err != nil {
			return err
		}
		all := a.sm.CheckUpdates(ctx, list)
		for _, s := range list {
			upd := "up to date"
			ups := all[s.Name]
			var newer, failed []string
			for _, u := range ups {
				if u.Err != nil {
					failed = append(failed, u.Image)
				} else {
					newer = append(newer, u.Image)
				}
			}
			switch {
			case len(newer) > 0:
				upd = "newer: " + strings.Join(newer, ", ")
			case len(failed) > 0:
				upd = "could not check: " + strings.Join(failed, ", ")
			case len(s.Containers) == 0:
				upd = "-"
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", s.ID(), len(s.Containers), s.Health(), upd)
		}
	}
	return w.Flush()
}

// ---- stacks ---------------------------------------------------------------------

func (a *app) findStack(ctx context.Context, id string) (stack.Stack, error) {
	nodeName, name, ok := strings.Cut(id, "/")
	if !ok {
		return stack.Stack{}, fmt.Errorf("stacks are written <node>/<stack>, e.g. %s/myapp", a.cfg.Nodes[0].Name)
	}
	n, err := a.cfg.Node(nodeName)
	if err != nil {
		return stack.Stack{}, err
	}
	st, err := (&node.Client{R: a.r, Name: n.Name}).Status(ctx)
	if err != nil {
		return stack.Stack{}, err
	}
	list, err := a.sm.List(ctx, n.Name, n.StacksDir, st)
	if err != nil {
		return stack.Stack{}, err
	}
	var names []string
	for _, s := range list {
		if s.Name == name {
			return s, nil
		}
		names = append(names, s.Name)
	}
	return stack.Stack{}, fmt.Errorf("no stack %q on %s (have: %s)", name, n.Name, strings.Join(names, ", "))
}

func (a *app) stackCmd(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: home stack <restart|stop|start|logs|update|rollback> <node>/<stack> [service…]")
	}
	action := args[0]
	fs := flag.NewFlagSet("stack "+action, flag.ExitOnError)
	follow := fs.Bool("f", false, "follow logs")
	tail := fs.Int("tail", 100, "log lines")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	fs.Parse(reorder(args[1:]))
	s, err := a.findStack(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	svc := fs.Args()[1:]
	switch action {
	case "restart":
		return a.sm.Restart(ctx, s, svc, os.Stdout)
	case "stop":
		if err := confirm(*yes, "Stop "+s.ID()+"?"); err != nil {
			return err
		}
		return a.sm.Stop(ctx, s, os.Stdout)
	case "start":
		return a.sm.Start(ctx, s, os.Stdout)
	case "logs":
		return a.sm.Logs(ctx, s, *follow, *tail, svc, os.Stdout)
	case "update":
		if err := confirm(*yes, "Update "+s.ID()+" to its newest images?"); err != nil {
			return err
		}
		changed, err := a.sm.Update(ctx, s, a.hook(ctx, s.ID()), os.Stdout)
		if err != nil {
			notify.Send("home: "+s.ID()+" update failed", err.Error())
			return err
		}
		if len(changed) > 0 {
			notify.Send("home: "+s.ID()+" updated ✓", strings.Join(changed, ", "))
		}
		return nil
	case "rollback":
		if err := confirm(*yes, "Roll "+s.ID()+" back to the images before its last update?"); err != nil {
			return err
		}
		return a.sm.Rollback(ctx, s, os.Stdout)
	}
	return fmt.Errorf("unknown stack action %q", action)
}

// hook returns the configured pre-update commands for a stack, run as
// `home <args>` so backups use the same binary and config.
func (a *app) hook(ctx context.Context, id string) func() error {
	cmds := a.cfg.Hooks[id]
	if len(cmds) == 0 {
		return nil
	}
	return func() error {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		for _, c := range cmds {
			logf("%s: pre-update: home %s", id, c)
			cmd := exec.CommandContext(ctx, self, strings.Fields(c)...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("`home %s`: %w", c, err)
			}
		}
		return nil
	}
}

// ---- nodes ------------------------------------------------------------------------

func (a *app) pkgCmd(ctx context.Context, kind string, args []string) error {
	fs := flag.NewFlagSet(kind, flag.ExitOnError)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	fs.Parse(reorder(args))
	if fs.NArg() != 2 || (fs.Arg(0) != "check" && fs.Arg(0) != "upgrade") {
		return fmt.Errorf("usage: home %s check|upgrade <node|all>", kind)
	}
	nodes, err := a.cfg.Ordered(fs.Arg(1))
	if err != nil {
		return err
	}
	for _, n := range nodes {
		c := &node.Client{R: a.r, Name: n.Name}
		logf("%s: refreshing package information", n.Name)
		if err := c.Refresh(ctx); err != nil {
			return err
		}
		st, err := c.Status(ctx)
		if err != nil {
			return err
		}
		if kind == "apt" {
			pending, err := c.AptPending(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("%s: %d apt updates\n", n.Name, len(pending))
			for _, p := range pending {
				fmt.Println("   ", p)
			}
			if fs.Arg(0) == "upgrade" && len(pending) > 0 {
				if err := confirm(*yes, fmt.Sprintf("Install %d updates on %s?", len(pending), n.Name)); err != nil {
					return err
				}
				if err := c.AptUpgrade(ctx, os.Stdout); err != nil {
					return err
				}
				if st2, _ := c.Status(ctx); st2 != nil && st2.RebootRequired {
					fmt.Printf("%s: a reboot is required (`home node reboot %s`)\n", n.Name, n.Name)
				}
			}
		} else {
			if st.DietPiUpdate == "" {
				fmt.Printf("%s: DietPi %s is current\n", n.Name, st.DietPi)
				continue
			}
			fmt.Printf("%s: DietPi %s → %s available\n", n.Name, st.DietPi, st.DietPiUpdate)
			if fs.Arg(0) == "upgrade" {
				if err := confirm(*yes, "Update DietPi on "+n.Name+"?"); err != nil {
					return err
				}
				if err := c.DietPiUpgrade(ctx, os.Stdout); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *app) nodeCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	fs.Parse(reorder(args))
	if fs.NArg() != 2 || fs.Arg(0) != "reboot" {
		return errors.New("usage: home node reboot <node>")
	}
	n, err := a.cfg.Node(fs.Arg(1))
	if err != nil {
		return err
	}
	if err := confirm(*yes, "Reboot "+n.Name+"? Its stacks will be down for a few minutes."); err != nil {
		return err
	}
	c := &node.Client{R: a.r, Name: n.Name}
	if err := c.Reboot(ctx, logf); err != nil {
		notify.Send("home: "+n.Name+" reboot problem", err.Error())
		return err
	}
	logf("%s: rebooted; every container is back and healthy", n.Name)
	return nil
}

// upgrade runs apt → DietPi → stack updates → reboot (if required), one
// node at a time in upgrade order, stopping at the first failure.
func (a *app) upgrade(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	dry := fs.Bool("dry-run", false, "show what would be done")
	noReboot := fs.Bool("no-reboot", false, "never reboot")
	fs.Parse(reorder(args))
	target := "all"
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}
	nodes, err := a.cfg.Ordered(target)
	if err != nil {
		return err
	}
	reports := check.Run(ctx, a.cfg, a.r, true, true)
	byNode := map[string]check.NodeReport{}
	for _, r := range reports {
		byNode[r.Node] = r
	}
	fmt.Println("Plan:")
	var plan []string
	for i, n := range nodes {
		r := byNode[n.Name]
		if r.Err != nil {
			return fmt.Errorf("%s: %w", n.Name, r.Err)
		}
		steps := []string{}
		if r.Status.AptUpgradable > 0 {
			steps = append(steps, fmt.Sprintf("apt (%d)", r.Status.AptUpgradable))
		}
		if r.Status.DietPiUpdate != "" {
			steps = append(steps, "DietPi → "+r.Status.DietPiUpdate)
		}
		var names []string
		for s := range r.Updates {
			names = append(names, s)
		}
		sort.Strings(names)
		if len(names) > 0 {
			steps = append(steps, "update "+strings.Join(names, ", "))
		}
		if a.cfg.Upgrade.Reboot() && !*noReboot {
			steps = append(steps, "reboot if required")
		}
		line := fmt.Sprintf("  %d. %s: %s", i+1, n.Name, strings.Join(steps, " → "))
		if len(steps) == 0 || (len(steps) == 1 && strings.HasPrefix(steps[0], "reboot") && !r.Status.RebootRequired) {
			line = fmt.Sprintf("  %d. %s: nothing to do", i+1, n.Name)
		}
		fmt.Println(line)
		plan = append(plan, line)
	}
	if *dry {
		fmt.Println("\nDry run: nothing changed.")
		return nil
	}
	if err := confirm(*yes, "\nProceed?"); err != nil {
		return err
	}
	var done []string
	for _, n := range nodes {
		r := byNode[n.Name]
		c := &node.Client{R: a.r, Name: n.Name}
		fail := func(step string, err error) error {
			msg := fmt.Sprintf("%s: %s failed: %v", n.Name, step, err)
			notify.Send("home: upgrade stopped", msg)
			return errors.New(msg)
		}
		if r.Status.AptUpgradable > 0 {
			logf("%s: apt upgrade", n.Name)
			if err := c.AptUpgrade(ctx, io.Discard); err != nil {
				return fail("apt upgrade", err)
			}
			done = append(done, n.Name+": apt")
		}
		if r.Status.DietPiUpdate != "" {
			logf("%s: DietPi update to %s", n.Name, r.Status.DietPiUpdate)
			if err := c.DietPiUpgrade(ctx, io.Discard); err != nil {
				return fail("DietPi update", err)
			}
			done = append(done, n.Name+": DietPi "+r.Status.DietPiUpdate)
		}
		var names []string
		for s := range r.Updates {
			names = append(names, s)
		}
		sort.Strings(names)
		for _, name := range names {
			s, err := a.findStack(ctx, n.Name+"/"+name)
			if err != nil {
				return fail("stack "+name, err)
			}
			changed, err := a.sm.Update(ctx, s, a.hook(ctx, s.ID()), io.Discard)
			if err != nil {
				return fail("update of "+s.ID(), err)
			}
			if len(changed) > 0 {
				done = append(done, s.ID()+" updated")
			}
		}
		if a.cfg.Upgrade.Reboot() && !*noReboot {
			st, err := c.Status(ctx)
			if err == nil && st.RebootRequired {
				if err := c.Reboot(ctx, logf); err != nil {
					return fail("reboot", err)
				}
				done = append(done, n.Name+": rebooted")
			}
		}
	}
	summary := "Nothing needed doing."
	if len(done) > 0 {
		summary = strings.Join(done, "\n")
	}
	fmt.Println("\n✓ Upgrade complete:\n" + summary)
	notify.Send("home: upgrade complete ✓", summary)
	return nil
}

// ---- helpers ------------------------------------------------------------------------

func confirm(yes bool, q string) error {
	if yes {
		return nil
	}
	ok, err := prompt.YesNo(q, false)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("cancelled")
	}
	return nil
}

// reorder moves flags before positional args so `home stack logs ab/x -f` works.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if args[i] == "--tail" || args[i] == "-tail" {
				if i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
			continue
		}
		pos = append(pos, args[i])
	}
	return append(flags, pos...)
}

func short(osName string) string {
	return strings.TrimSuffix(strings.TrimPrefix(osName, "Debian GNU/Linux "), "")
}

func human(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

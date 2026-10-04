// Package homesetup is the `home setup` wizard for nodes and stacks.
package homesetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	brconfig "github.com/ashishbhatiya18/home/internal/br/config"
	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/prompt"
	"github.com/ashishbhatiya18/home/internal/remote"
)

func say(f string, a ...any) { fmt.Printf(f+"\n", a...) }

// Run creates or replaces the home config at path.
func Run(ctx context.Context, path string) error {
	say("\nhome setup — nodes and stacks\n")
	if _, err := os.Stat(path); err == nil {
		ok, err := prompt.YesNo("A config already exists at "+path+". Replace it (old one kept as .prev)?", false)
		if err != nil || !ok {
			return errors.New("setup cancelled")
		}
	}
	r := &remote.Runner{Hosts: map[string]string{}, ConnectTimeout: 6 * time.Second}
	cfg := &homecfg.Config{}

	say("Add the machines (nodes) that run your Docker Compose stacks.")
	for {
		n, err := addNode(ctx, r)
		if err != nil {
			say("  ✗ %v", err)
		} else {
			cfg.Nodes = append(cfg.Nodes, *n)
		}
		more, err := prompt.YesNo("\nAdd another node?", false)
		if err != nil {
			return err
		}
		if !more && len(cfg.Nodes) > 0 {
			break
		}
	}

	if len(cfg.Nodes) > 1 {
		var names []string
		for _, n := range cfg.Nodes {
			names = append(names, n.Name)
		}
		say("\nDuring `home upgrade`, nodes are upgraded and rebooted one after another.")
		say("Put nodes that host shared services (databases, reverse proxy) last.")
		order, err := prompt.Ask("Upgrade order (comma-separated)", strings.Join(names, ","))
		if err != nil {
			return err
		}
		for _, o := range strings.Split(order, ",") {
			if o = strings.TrimSpace(o); o != "" {
				cfg.Upgrade.Order = append(cfg.Upgrade.Order, o)
			}
		}
	}

	t, err := prompt.Ask("\nDaily node check time (HH:MM) — you get one notification summarising updates, reboots and problems", "09:00")
	if err != nil {
		return err
	}
	cfg.Checks.DailyAt = t
	cfg.Checks.DiskWarnPercent = 80

	if err := suggestHooks(ctx, r, cfg); err != nil {
		return err
	}
	if err := homecfg.Save(path, cfg); err != nil {
		return err
	}
	say("\n✓ Saved %s", path)
	return nil
}

func addNode(ctx context.Context, r *remote.Runner) (*homecfg.Node, error) {
	target, err := prompt.Ask("\nSSH target (user@host or ~/.ssh/config alias)", "")
	if err != nil || target == "" {
		return nil, errors.New("no target given")
	}
	tmp := "probe"
	r.Hosts[tmp] = target
	say("  checking SSH, sudo and Docker on %s …", target)
	if !r.Reachable(ctx, tmp) {
		return nil, fmt.Errorf("cannot log in without a password; run `ssh-copy-id %s` first", target)
	}
	if err := r.Run(ctx, tmp, "sudo -n true", nil, nil); err != nil {
		say("  ! passwordless sudo is not available: apt/DietPi upgrades and reboots will fail.")
		say("    Allow it with a sudoers entry for this user (e.g. `user ALL=(ALL) NOPASSWD: ALL`).")
	} else {
		say("  ✓ passwordless sudo")
	}
	if err := r.Run(ctx, tmp, "docker ps -q >/dev/null", nil, nil); err != nil {
		return nil, errors.New("this user cannot run docker (add it to the docker group)")
	}
	say("  ✓ docker")
	host, _ := r.Output(ctx, tmp, "hostname -s")
	name, err := prompt.Ask("  Node name", strings.TrimSpace(host))
	if err != nil {
		return nil, err
	}

	// Find directories whose subdirectories hold compose files.
	out, _ := r.Output(ctx, tmp, `find "$HOME" /opt /srv -maxdepth 6 \( -name node_modules -o -name .git \) -prune -o \( -name compose.yaml -o -name docker-compose.yml \) -print 2>/dev/null | xargs -r -n1 dirname | xargs -r -n1 dirname | sort | uniq -c | sort -rn | head -5`)
	var dirs []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			dirs = append(dirs, f[1])
		}
	}
	var dir string
	if len(dirs) > 0 {
		opts := append(append([]string{}, dirs...), "Other…")
		i, err := prompt.Choose("  Which directory holds your stacks (one subdirectory per stack)?", opts, 0)
		if err != nil {
			return nil, err
		}
		if i < len(dirs) {
			dir = dirs[i]
		}
	}
	if dir == "" {
		if dir, err = prompt.Ask("  Stacks directory", ""); err != nil {
			return nil, err
		}
	}
	list, _ := r.Output(ctx, tmp, fmt.Sprintf(`for d in %s/*/; do [ -f "$d/compose.yaml" ] && basename "$d"; done`, remote.Quote(dir)))
	stacks := strings.Fields(list)
	say("  ✓ %d stacks: %s", len(stacks), strings.Join(stacks, ", "))
	delete(r.Hosts, tmp)
	r.Hosts[name] = target
	return &homecfg.Node{Name: name, SSH: target, StacksDir: dir}, nil
}

// suggestHooks offers a `br backup` before updating stacks whose name
// matches a backed-up app.
func suggestHooks(ctx context.Context, r *remote.Runner, cfg *homecfg.Config) error {
	bc, err := brconfig.Load(brconfig.DefaultPath())
	if err != nil {
		return nil // backups not configured
	}
	apps := map[string]bool{}
	for _, a := range bc.Apps {
		apps[a.Name] = true
	}
	var matches []string
	for _, n := range cfg.Nodes {
		out, _ := r.Output(ctx, n.Name, fmt.Sprintf(`for d in %s/*/; do [ -f "$d/compose.yaml" ] && basename "$d"; done`, remote.Quote(n.StacksDir)))
		for _, s := range strings.Fields(out) {
			if apps[s] {
				matches = append(matches, n.Name+"/"+s)
			}
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	say("\nThese stacks have database backups configured (home br): %s", strings.Join(matches, ", "))
	ok, err := prompt.YesNo("Take a fresh backup automatically before updating them?", true)
	if err != nil || !ok {
		return err
	}
	cfg.Hooks = map[string][]string{}
	for _, id := range matches {
		_, app, _ := strings.Cut(id, "/")
		cfg.Hooks[id] = []string{"br backup " + app}
	}
	return nil
}

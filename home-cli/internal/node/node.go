// Package node inspects and maintains a Debian/DietPi host over SSH: status,
// apt and DietPi upgrades, and reboots that wait for every container to come
// back.
package node

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ashishbhatiya18/home/internal/remote"
)

type Client struct {
	R    *remote.Runner
	Name string
}

// Container is one container as seen by `docker ps -a`.
type Container struct {
	Name, Image, State, Status, Project string
}

func (c Container) Healthy() bool {
	return c.State == "running" && !strings.Contains(c.Status, "unhealthy") && !strings.Contains(c.Status, "Restarting")
}

type Status struct {
	DietPi, DietPiUpdate, OS, Kernel string
	Uptime                           time.Duration
	RebootRequired                   bool
	AptUpgradable                    int
	DiskPercent, MemPercent          int
	Load                             string
	GitopsAgent                      string
	Containers                       []Container
}

const statusScript = `
. /boot/dietpi/.version 2>/dev/null && echo "dietpi=$G_DIETPI_VERSION_CORE.$G_DIETPI_VERSION_SUB.$G_DIETPI_VERSION_RC"
[ -f /run/dietpi/.update_available ] && echo "dietpi_update=$(cat /run/dietpi/.update_available)"
. /etc/os-release && echo "os=$PRETTY_NAME"
echo "kernel=$(uname -r)"
echo "uptime=$(cut -d. -f1 /proc/uptime)"
[ -f /var/run/reboot-required ] && echo reboot=1 || echo reboot=0
echo "apt=$(apt list --upgradable 2>/dev/null | grep -c upgradable)"
echo "disk=$(df --output=pcent / | tail -1 | tr -dc 0-9)"
echo "mem=$(free | awk '/^Mem:/{printf "%d", ($2-$7)*100/$2}')"
echo "load=$(cut -d' ' -f1-3 /proc/loadavg)"
# Only nodes that still have the legacy agent installed report it.
systemctl cat gitops-agent >/dev/null 2>&1 && echo "gitops=$(systemctl is-active gitops-agent 2>/dev/null || true)"
docker ps -a --format 'ctr={{.Names}}|{{.Image}}|{{.State}}|{{.Status}}|{{.Label "com.docker.compose.project"}}'
`

func (c *Client) Status(ctx context.Context) (*Status, error) {
	out, err := c.R.Output(ctx, c.Name, "bash -s <<'HOME_EOF'\n"+statusScript+"HOME_EOF")
	if err != nil {
		return nil, err
	}
	s := &Status{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "dietpi":
			s.DietPi = v
		case "dietpi_update":
			s.DietPiUpdate = strings.TrimSpace(v)
		case "os":
			s.OS = v
		case "kernel":
			s.Kernel = v
		case "uptime":
			n, _ := strconv.Atoi(v)
			s.Uptime = time.Duration(n) * time.Second
		case "reboot":
			s.RebootRequired = v == "1"
		case "apt":
			s.AptUpgradable, _ = strconv.Atoi(v)
		case "disk":
			s.DiskPercent, _ = strconv.Atoi(v)
		case "mem":
			s.MemPercent, _ = strconv.Atoi(v)
		case "load":
			s.Load = v
		case "gitops":
			s.GitopsAgent = v
		case "ctr":
			f := strings.SplitN(v, "|", 5)
			if len(f) == 5 {
				s.Containers = append(s.Containers, Container{Name: f[0], Image: f[1], State: f[2], Status: f[3], Project: f[4]})
			}
		}
	}
	sort.Slice(s.Containers, func(i, j int) bool { return s.Containers[i].Name < s.Containers[j].Name })
	return s, nil
}

// Problems lists containers that are running but unhealthy or restarting.
func (s *Status) Problems() []string {
	var p []string
	for _, c := range s.Containers {
		if c.State == "restarting" || strings.Contains(c.Status, "unhealthy") || strings.Contains(c.Status, "Restarting") {
			p = append(p, fmt.Sprintf("%s (%s)", c.Name, c.Status))
		}
	}
	return p
}

// Refresh updates the apt package lists and DietPi's update check, so Status
// reports what is actually available.
func (c *Client) Refresh(ctx context.Context) error {
	return c.R.Run(ctx, c.Name, "sudo -n apt-get update -qq >/dev/null && "+
		"{ [ ! -x /boot/dietpi/dietpi-update ] || sudo -n G_INTERACTIVE=0 /boot/dietpi/dietpi-update 2 >/dev/null 2>&1 || true; }", nil, nil)
}

// AptPending lists upgradable packages.
func (c *Client) AptPending(ctx context.Context) ([]string, error) {
	out, err := c.R.Output(ctx, c.Name, "apt list --upgradable 2>/dev/null | tail -n +2")
	if err != nil {
		return nil, err
	}
	var p []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			p = append(p, l)
		}
	}
	return p, nil
}

// AptUpgrade installs all upgrades non-interactively, keeping existing
// config files, then removes packages no longer needed.
func (c *Client) AptUpgrade(ctx context.Context, out io.Writer) error {
	cmd := "sudo -n DEBIAN_FRONTEND=noninteractive apt-get -y -q " +
		"-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold full-upgrade && " +
		"sudo -n DEBIAN_FRONTEND=noninteractive apt-get -y -q autoremove"
	return c.R.Run(ctx, c.Name, cmd, nil, out)
}

// DietPiUpgrade runs DietPi's own updater non-interactively.
func (c *Client) DietPiUpgrade(ctx context.Context, out io.Writer) error {
	return c.R.Run(ctx, c.Name, "sudo -n G_INTERACTIVE=0 /boot/dietpi/dietpi-update 1", nil, out)
}

// Reboot restarts the node, waits for it to go down and come back, then for
// every container that was running before to be running and healthy again.
func (c *Client) Reboot(ctx context.Context, logf func(string, ...any)) error {
	before, err := c.Status(ctx)
	if err != nil {
		return err
	}
	running := map[string]bool{}
	for _, ct := range before.Containers {
		if ct.State == "running" {
			running[ct.Name] = true
		}
	}
	bootBefore := before.Uptime
	logf("%s: rebooting (%d containers running)", c.Name, len(running))
	_ = c.R.Run(ctx, c.Name, "sudo -n systemd-run --on-active=2 systemctl reboot", nil, nil)

	// Wait until the node is back with a fresh uptime.
	deadline := time.Now().Add(10 * time.Minute)
	time.Sleep(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not come back within 10 minutes", c.Name)
		}
		if c.R.Reachable(ctx, c.Name) {
			if st, err := c.Status(ctx); err == nil && st.Uptime < bootBefore && st.Uptime < 10*time.Minute {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	logf("%s: back up; waiting for containers", c.Name)
	return c.WaitContainers(ctx, running, 5*time.Minute, logf)
}

// WaitContainers waits until every named container is running and healthy.
func (c *Client) WaitContainers(ctx context.Context, want map[string]bool, timeout time.Duration, logf func(string, ...any)) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := c.Status(ctx)
		if err == nil {
			var missing []string
			have := map[string]Container{}
			for _, ct := range st.Containers {
				have[ct.Name] = ct
			}
			for n := range want {
				if ct, ok := have[n]; !ok || !ct.Healthy() || strings.Contains(ct.Status, "health: starting") {
					missing = append(missing, n)
				}
			}
			if len(missing) == 0 {
				return nil
			}
			if time.Now().After(deadline) {
				sort.Strings(missing)
				return fmt.Errorf("%s: not healthy after %s: %s", c.Name, timeout, strings.Join(missing, ", "))
			}
		} else if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

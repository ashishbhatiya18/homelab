// Package homecfg loads ~/.config/home/config.yaml: the nodes and stacks
// `home` manages. Created by `home setup`; holds no secrets.
package homecfg

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

func DefaultPath() string {
	if p := os.Getenv("HOME_CONFIG"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "home", "config.yaml")
}

func StateDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "home")
}

type Config struct {
	Nodes   []Node  `yaml:"nodes"`
	Upgrade Upgrade `yaml:"upgrade"`
	Checks  Checks  `yaml:"checks"`
	Jobs    Jobs    `yaml:"jobs,omitempty"`
	// Hooks run before a stack update, keyed by "node/stack". Each entry is a
	// `home` command line, e.g. "br backup myapp".
	Hooks map[string][]string `yaml:"pre_update_hooks,omitempty"`
}

type Node struct {
	Name string `yaml:"name"`
	SSH  string `yaml:"ssh"`
	// StacksDir holds one directory per stack, each with a compose file.
	StacksDir string `yaml:"stacks_dir"`
	// Bundle is the image `home deploy` rolls out to this node (e.g.
	// ghcr.io/me/node-ab): a FROM-scratch image holding /stacks, deployed to
	// StacksDir; releases are kept next to it. Empty: no bundle deploys.
	Bundle string `yaml:"bundle,omitempty"`
}

type Upgrade struct {
	// Order nodes are upgraded/rebooted in; put nodes that host shared
	// services (databases, proxies) last.
	Order []string `yaml:"order,flow"`
	// RebootIfRequired reboots a node at the end of `home upgrade` when the
	// OS asks for it.
	RebootIfRequired *bool `yaml:"reboot_if_required,omitempty"`
}

func (u Upgrade) Reboot() bool { return u.RebootIfRequired == nil || *u.RebootIfRequired }

type Checks struct {
	DailyAt         string `yaml:"daily_at"`
	DiskWarnPercent int    `yaml:"disk_warn_percent"`
	// NotifyWhenClear also notifies when nothing needs attention.
	NotifyWhenClear *bool `yaml:"notify_when_clear,omitempty"`
}

// Jobs switches the background service's optional jobs (`home run`, every
// 15 minutes) on or off; backups and the daily check always run.
type Jobs struct {
	// Deploy rolls out new node bundles on every run (default on; only
	// nodes with a bundle are affected).
	Deploy *bool `yaml:"deploy,omitempty"`
	// Cleanup reclaims unused images and caches on every node once a day,
	// after checks.daily_at (default off).
	Cleanup *bool `yaml:"cleanup,omitempty"`
}

func (j Jobs) DeployOn() bool  { return j.Deploy == nil || *j.Deploy }
func (j Jobs) CleanupOn() bool { return j.Cleanup != nil && *j.Cleanup }

func (c Checks) WhenClear() bool { return c.NotifyWhenClear == nil || *c.NotifyWhenClear }

// DueSince returns the most recent scheduled check time at or before now.
func (c Checks) DueSince(now time.Time) time.Time {
	h, m := 9, 0
	fmt.Sscanf(c.DailyAt, "%d:%d", &h, &m)
	t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no config at %s — run `home setup`", path)
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Checks.DailyAt == "" {
		c.Checks.DailyAt = "09:00"
	}
	if c.Checks.DiskWarnPercent == 0 {
		c.Checks.DiskWarnPercent = 80
	}
	if len(c.Nodes) == 0 {
		return nil, errors.New("no nodes configured — run `home setup`")
	}
	seen := map[string]bool{}
	for _, n := range c.Nodes {
		if n.Name == "" || n.SSH == "" || n.StacksDir == "" {
			return nil, fmt.Errorf("node %q needs name, ssh and stacks_dir", n.Name)
		}
		if seen[n.Name] {
			return nil, fmt.Errorf("duplicate node %q", n.Name)
		}
		seen[n.Name] = true
	}
	for _, o := range c.Upgrade.Order {
		if !seen[o] {
			return nil, fmt.Errorf("upgrade.order names unknown node %q", o)
		}
	}
	return &c, nil
}

// Node returns the named node.
func (c *Config) Node(name string) (*Node, error) {
	for i := range c.Nodes {
		if c.Nodes[i].Name == name {
			return &c.Nodes[i], nil
		}
	}
	return nil, fmt.Errorf("unknown node %q", name)
}

// Ordered returns nodes in upgrade order (unlisted nodes first, in config order).
func (c *Config) Ordered(only string) ([]Node, error) {
	if only != "" && only != "all" {
		n, err := c.Node(only)
		if err != nil {
			return nil, err
		}
		return []Node{*n}, nil
	}
	pos := map[string]int{}
	for i, n := range c.Upgrade.Order {
		pos[n] = i + 1
	}
	var first, rest []Node
	for _, n := range c.Nodes {
		if pos[n.Name] == 0 {
			first = append(first, n)
		}
	}
	for _, name := range c.Upgrade.Order {
		n, _ := c.Node(name)
		rest = append(rest, *n)
	}
	return append(first, rest...), nil
}

// Hosts maps node names to SSH targets for remote.Runner.
func (c *Config) Hosts() map[string]string {
	m := map[string]string{}
	for _, n := range c.Nodes {
		m[n.Name] = n.SSH
	}
	return m
}

const header = "# home configuration — created by `home setup`. No secrets here.\n" +
	"# Backups (home br) are configured separately in br.yaml.\n\n"

func Save(path string, c *Config) error {
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		os.WriteFile(path+".prev", old, 0o600)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	_, err := Load(path)
	return err
}

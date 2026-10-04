// Package config loads the user's hbr configuration.
//
// The config file lives outside the repository (default
// ~/.config/hbr/config.yaml) and holds every environment-specific
// value: hosts, paths, app definitions. Nothing sensitive is compiled in.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is used when neither --config nor HBR_CONFIG is set.
func DefaultPath() string {
	if p := os.Getenv("HBR_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(home(), ".config", "hbr", "config.yaml")
}

type Config struct {
	Destination string          `yaml:"destination"`
	Schedule    Schedule        `yaml:"schedule"`
	KeysDir     string          `yaml:"keys_dir"`
	StateDir    string          `yaml:"state_dir"`
	SSH         SSH             `yaml:"ssh"`
	Hosts       map[string]Host `yaml:"hosts"`
	Retention   Retention       `yaml:"retention"`
	Alerts      Alerts          `yaml:"alerts"`
	Verify      Verify          `yaml:"verify"`
	Tools       Tools           `yaml:"tools"`
	Apps        []App           `yaml:"apps"`
}

type Schedule struct {
	// DailyAt is the local time (HH:MM) after which the day's backup is due.
	// If the machine is asleep or the server unreachable then, the backup runs
	// at the next opportunity.
	DailyAt string `yaml:"daily_at"`
}

// DueSince returns the most recent scheduled time at or before now.
func (s Schedule) DueSince(now time.Time) time.Time {
	h, m := 2, 0
	fmt.Sscanf(s.DailyAt, "%d:%d", &h, &m)
	t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

type SSH struct {
	ConnectTimeout Duration `yaml:"connect_timeout"`
	Options        []string `yaml:"options"`
}

type Host struct {
	// SSH target, e.g. "user@10.0.0.5" or an alias from ~/.ssh/config.
	SSH string `yaml:"ssh"`
}

type Retention struct {
	Daily   int `yaml:"daily"`
	Weekly  int `yaml:"weekly"`
	Monthly int `yaml:"monthly"`
	MinKeep int `yaml:"min_keep"`
}

// SuccessNotifications reports whether to notify after successful backups.
func (a Alerts) SuccessNotifications() bool { return a.NotifySuccess == nil || *a.NotifySuccess }

type Alerts struct {
	// NotifySuccess shows a notification after every successful backup.
	// Omitted means on.
	NotifySuccess *bool    `yaml:"notify_success"`
	StaleAfter    Duration `yaml:"stale_after"`
	RepeatEvery   Duration `yaml:"repeat_every"`
	DrillEvery    Duration `yaml:"drill_every"`
}

type Verify struct {
	// Local container CLI used for restore drills (docker or podman).
	ContainerCLI string `yaml:"container_cli"`
	// Postgres major versions each drill restores into; 0 = the backup's own.
	PostgresMajors []int `yaml:"postgres_majors"`
}

type Tools struct {
	// Directory with local Postgres client tools (pg_restore, psql, pg_dump).
	PgBin string `yaml:"pg_bin"`
}

type App struct {
	Name    string      `yaml:"name"`
	Sources []yaml.Node `yaml:"sources"`
	Restore Restore     `yaml:"restore"`
}

type Restore struct {
	Stop   []StopSpec `yaml:"stop"`
	Health Health     `yaml:"health"`
}

type StopSpec struct {
	Host       string   `yaml:"host"`
	Containers []string `yaml:"containers"`
}

type Health struct {
	URL     string   `yaml:"url"`
	Timeout Duration `yaml:"timeout"`
}

// Duration accepts Go duration strings ("48h", "90s") in YAML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config not found at %s (copy config.example.yaml there to start)", path)
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	c.Destination = Expand(c.Destination)
	if c.KeysDir == "" {
		c.KeysDir = filepath.Join(c.Destination, "keys")
	}
	c.KeysDir = Expand(c.KeysDir)
	if c.StateDir == "" {
		c.StateDir = filepath.Join(home(), ".local", "state", "hbr")
	}
	c.StateDir = Expand(c.StateDir)
	if c.Schedule.DailyAt == "" {
		c.Schedule.DailyAt = "02:00"
	}
	if c.SSH.ConnectTimeout.Duration == 0 {
		c.SSH.ConnectTimeout.Duration = 5 * time.Second
	}
	r := &c.Retention
	if r.Daily == 0 && r.Weekly == 0 && r.Monthly == 0 {
		r.Daily, r.Weekly, r.Monthly = 7, 4, 12
	}
	if r.MinKeep < 3 {
		r.MinKeep = 3
	}
	if c.Alerts.StaleAfter.Duration == 0 {
		c.Alerts.StaleAfter.Duration = 48 * time.Hour
	}
	if c.Alerts.RepeatEvery.Duration == 0 {
		c.Alerts.RepeatEvery.Duration = 6 * time.Hour
	}
	if c.Alerts.DrillEvery.Duration == 0 {
		c.Alerts.DrillEvery.Duration = 30 * 24 * time.Hour
	}
	if c.Verify.ContainerCLI == "" {
		c.Verify.ContainerCLI = "docker"
	}
	if len(c.Verify.PostgresMajors) == 0 {
		c.Verify.PostgresMajors = []int{0}
	}
	if c.Tools.PgBin == "" {
		c.Tools.PgBin = "/opt/homebrew/opt/libpq/bin"
	}
	c.Tools.PgBin = Expand(c.Tools.PgBin)
	for i := range c.Apps {
		if c.Apps[i].Restore.Health.Timeout.Duration == 0 {
			c.Apps[i].Restore.Health.Timeout.Duration = 2 * time.Minute
		}
	}
}

func (c *Config) validate() error {
	if c.Destination == "" {
		return errors.New("destination is required")
	}
	var h, m int
	if n, _ := fmt.Sscanf(c.Schedule.DailyAt, "%d:%d", &h, &m); n != 2 || h > 23 || m > 59 {
		return fmt.Errorf("schedule.daily_at must be HH:MM, got %q", c.Schedule.DailyAt)
	}
	if len(c.Apps) == 0 {
		return errors.New("at least one app is required")
	}
	seen := map[string]bool{}
	for _, a := range c.Apps {
		if a.Name == "" || strings.ContainsAny(a.Name, "/ ") {
			return fmt.Errorf("invalid app name %q", a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("duplicate app %q", a.Name)
		}
		seen[a.Name] = true
		if len(a.Sources) == 0 {
			return fmt.Errorf("app %q has no sources", a.Name)
		}
		for _, s := range a.Restore.Stop {
			if _, ok := c.Hosts[s.Host]; !ok {
				return fmt.Errorf("app %q: restore.stop references unknown host %q", a.Name, s.Host)
			}
		}
	}
	return nil
}

// App returns the named app.
func (c *Config) App(name string) (*App, error) {
	for i := range c.Apps {
		if c.Apps[i].Name == name {
			return &c.Apps[i], nil
		}
	}
	return nil, fmt.Errorf("unknown app %q", name)
}

// Expand resolves a leading ~ to the user's home directory.
func Expand(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home(), p[2:])
	}
	return p
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// Package service registers hbr as a background service through Homebrew,
// so its lifecycle is managed with `brew services` (like `cloudflared
// service install`).
package service

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
)

const formula = "home"

func brew() (string, error) {
	p, err := exec.LookPath("brew")
	if err != nil {
		return "", errors.New("Homebrew not found; home's service is managed by `brew services`")
	}
	return p, nil
}

func installedViaBrew(b string) bool {
	return exec.Command(b, "list", "--formula", formula).Run() == nil
}

func brewRun(b string, args ...string) error {
	c := exec.Command(b, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// Install starts (or restarts) the service. It runs at login and checks every
// 15 minutes whether a backup is due.
func Install() error {
	b, err := brew()
	if err != nil {
		return err
	}
	if !installedViaBrew(b) {
		return errors.New("home was not installed with Homebrew, so `brew services` cannot manage it.\n" +
			"Install it with: brew install ashishbhatiya18/tap/home")
	}
	if Running() {
		return brewRun(b, "services", "restart", formula)
	}
	return brewRun(b, "services", "start", formula)
}

// Uninstall stops the service and removes it from login items. Backups,
// config and keys are left untouched.
func Uninstall() error {
	b, err := brew()
	if err != nil {
		return err
	}
	return brewRun(b, "services", "stop", formula)
}

// Running reports whether the service is loaded (registered with launchd).
// Being an interval job, it is usually loaded but not executing.
func Running() bool {
	b, err := brew()
	if err != nil {
		return false
	}
	out, err := exec.Command(b, "services", "info", formula, "--json").Output()
	if err != nil {
		return false
	}
	return loaded(out)
}

func loaded(infoJSON []byte) bool {
	var info []struct {
		Loaded bool `json:"loaded"`
	}
	if err := json.Unmarshal(infoJSON, &info); err != nil {
		return false
	}
	return len(info) > 0 && info[0].Loaded
}

// Describe returns a one-line status for `hbr status`.
func Describe() string {
	b, err := brew()
	if err != nil {
		return "service: Homebrew not found"
	}
	if !installedViaBrew(b) {
		return "service: not available (home not installed via Homebrew)"
	}
	if Running() {
		return "service: running (manage with `brew services info|stop|restart home`)"
	}
	return "service: not installed — run `home install`"
}

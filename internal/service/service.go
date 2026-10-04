// Package service registers hbr as a background service through Homebrew,
// so its lifecycle is managed with `brew services` (like `cloudflared
// service install`).
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const formula = "hbr"

func brew() (string, error) {
	p, err := exec.LookPath("brew")
	if err != nil {
		return "", errors.New("Homebrew not found; hbr's service is managed by `brew services`")
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
		return errors.New("hbr was not installed with Homebrew, so `brew services` cannot manage it.\n" +
			"Install it with: brew install ashishbhatiya18/tap/hbr")
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

// Running reports whether the service is loaded.
func Running() bool {
	b, err := brew()
	if err != nil {
		return false
	}
	out, err := exec.Command(b, "services", "info", formula, "--json").Output()
	return err == nil && strings.Contains(string(out), `"loaded":true`)
}

// Describe returns a one-line status for `hbr status`.
func Describe() string {
	b, err := brew()
	if err != nil {
		return "service: Homebrew not found"
	}
	if !installedViaBrew(b) {
		return "service: not available (hbr not installed via Homebrew)"
	}
	if Running() {
		return "service: running (manage with `brew services info|stop|restart hbr`)"
	}
	return fmt.Sprintf("service: not installed — run `hbr install`")
}

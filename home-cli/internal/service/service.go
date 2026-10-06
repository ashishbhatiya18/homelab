// Package service manages home's background jobs as Homebrew services, so
// their lifecycle is `brew services` (like `cloudflared service install`).
//
// Homebrew allows one service per formula, so every job is a small companion
// formula in the tap (home-backup, home-check, home-cleanup, home-deploy)
// whose service runs `home jobs run <job> --if-due` on its own interval and
// writes its own log under $(brew --prefix)/var/log/home-<job>.log.
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const tap = "ashishbhatiya18/tap/"

// Jobs are the background jobs, in the order they are listed.
var Jobs = []string{"backup", "check", "cleanup", "deploy"}

// Formula is the companion formula (and brew service) of a job.
func Formula(job string) string { return "home-" + job }

func brew() (string, error) {
	p, err := exec.LookPath("brew")
	if err != nil {
		return "", errors.New("Homebrew not found; home's jobs are managed by `brew services`")
	}
	return p, nil
}

func installed(b, formula string) bool {
	return exec.Command(b, "list", "--formula", formula).Run() == nil
}

func brewRun(b string, args ...string) error {
	c := exec.Command(b, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// Install installs the companion formula of each job (if needed) and starts
// its service. No jobs means all of them.
func Install(jobs ...string) error {
	b, err := brew()
	if err != nil {
		return err
	}
	if !installed(b, "home") {
		return errors.New("home was not installed with Homebrew, so `brew services` cannot manage its jobs.\n" +
			"Install it with: brew install ashishbhatiya18/tap/home")
	}
	removeLegacy(b)
	if len(jobs) == 0 {
		jobs = Jobs
	}
	for _, j := range jobs {
		if err := valid(j); err != nil {
			return err
		}
		f := Formula(j)
		if !installed(b, f) {
			if err := brewRun(b, "install", "--quiet", tap+f); err != nil {
				return fmt.Errorf("installing %s: %w", f, err)
			}
		}
		action := "start"
		if Loaded(f) {
			action = "restart"
		}
		if err := brewRun(b, "services", action, f); err != nil {
			return fmt.Errorf("starting %s: %w", f, err)
		}
	}
	return nil
}

// Uninstall stops the services of the given jobs (all if none). Backups,
// config and keys are left untouched; the formulae stay installed.
func Uninstall(jobs ...string) error {
	b, err := brew()
	if err != nil {
		return err
	}
	removeLegacy(b)
	if len(jobs) == 0 {
		jobs = Jobs
	}
	for _, j := range jobs {
		if err := valid(j); err != nil {
			return err
		}
		if f := Formula(j); installed(b, f) && Loaded(f) {
			if err := brewRun(b, "services", "stop", f); err != nil {
				return err
			}
		}
	}
	return nil
}

func valid(job string) error {
	for _, j := range Jobs {
		if j == job {
			return nil
		}
	}
	return fmt.Errorf("unknown job %q (have: backup, check, cleanup, deploy)", job)
}

// removeLegacy unloads the single `home` service of versions before 0.5,
// which ran every job; the companion services replace it.
func removeLegacy(b string) {
	home, _ := os.UserHomeDir()
	plist := filepath.Join(home, "Library", "LaunchAgents", "sh.brew.home.plist")
	if _, err := os.Stat(plist); err != nil {
		return
	}
	if exec.Command(b, "services", "stop", "home").Run() == nil {
		return
	}
	// The formula no longer declares a service, so brew cannot stop it.
	exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), plist).Run()
	os.Remove(plist)
}

// Loaded reports whether a formula's service is loaded (registered with
// launchd). Interval jobs are usually loaded but not executing.
func Loaded(formula string) bool {
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

// Describe returns a one-line status of the backup job for `home br status`.
func Describe() string {
	b, err := brew()
	if err != nil {
		return "service: Homebrew not found"
	}
	if !installed(b, "home") {
		return "service: not available (home not installed via Homebrew)"
	}
	if Loaded(Formula("backup")) {
		return "service: home-backup running (brew services info home-backup)"
	}
	return "service: not running — run `home install backup`"
}

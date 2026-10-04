// Package engine orchestrates backups, drills and restores. It only talks to
// sources through the source.Source interface, so new kinds of data never
// require changes here.
package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"filippo.io/age"

	"github.com/ashishbhatiya18/hbr/internal/archive"
	"github.com/ashishbhatiya18/hbr/internal/config"
	"github.com/ashishbhatiya18/hbr/internal/keys"
	"github.com/ashishbhatiya18/hbr/internal/manifest"
	"github.com/ashishbhatiya18/hbr/internal/prompt"
	"github.com/ashishbhatiya18/hbr/internal/remote"
	"github.com/ashishbhatiya18/hbr/internal/source"
	"github.com/ashishbhatiya18/hbr/internal/state"
	"github.com/ashishbhatiya18/hbr/internal/store"
)

type Engine struct {
	Cfg     *config.Config
	Env     *source.Env
	State   *state.State
	Version string
	Log     *log.Logger
}

func New(cfg *config.Config, version string) (*Engine, error) {
	hosts := map[string]string{}
	for k, h := range cfg.Hosts {
		hosts[k] = h.SSH
	}
	st, err := state.Load(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	lg := log.New(os.Stdout, "", log.LstdFlags)
	e := &Engine{Cfg: cfg, State: st, Version: version, Log: lg}
	e.Env = &source.Env{
		Remote:       &remote.Runner{Hosts: hosts, ConnectTimeout: cfg.SSH.ConnectTimeout.Duration, ExtraOptions: cfg.SSH.Options},
		ContainerCLI: cfg.Verify.ContainerCLI,
		VerifyMajors: cfg.Verify.PostgresMajors,
		PgBin:        cfg.Tools.PgBin,
		Logf:         lg.Printf,
	}
	// Validate every app's sources up front so config errors surface early.
	for _, a := range cfg.Apps {
		if _, err := e.sources(a.Name); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *Engine) sources(app string) ([]source.Source, error) {
	a, err := e.Cfg.App(app)
	if err != nil {
		return nil, err
	}
	hosts := map[string]string{}
	for k, h := range e.Cfg.Hosts {
		hosts[k] = h.SSH
	}
	return source.Build(a.Name, a.Sources, hosts)
}

// identity unlocks a private key: the daily key via password, or the recovery key.
func (e *Engine) identity(recovery bool) (age.Identity, error) {
	if recovery {
		s, err := prompt.Password("Paste recovery key (AGE-SECRET-KEY-1…): ")
		if err != nil {
			return nil, err
		}
		return keys.ParseRecovery(s)
	}
	p, err := prompt.Password("Backup password: ")
	if err != nil {
		return nil, err
	}
	return keys.DailyIdentity(e.Cfg.KeysDir, p)
}

// open checks, decrypts and unpacks a backup into a private temp directory.
func (e *Engine) open(b store.Backup, id age.Identity) (string, *manifest.Manifest, func(), error) {
	if err := store.VerifyChecksum(b); err != nil {
		return "", nil, nil, err
	}
	f, err := os.Open(b.Path)
	if err != nil {
		return "", nil, nil, err
	}
	defer f.Close()
	dir, err := os.MkdirTemp("", "hbr-open-*")
	if err != nil {
		return "", nil, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	r, err := age.Decrypt(f, id)
	if err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("decrypt %s: %w", filepath.Base(b.Path), err)
	}
	if err := archive.Extract(r, dir); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("unpack: %w", err)
	}
	mb, err := os.ReadFile(filepath.Join(dir, manifest.FileName))
	if err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("manifest missing: %w", err)
	}
	var m manifest.Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("manifest unreadable: %w", err)
	}
	return dir, &m, cleanup, nil
}

func (e *Engine) appNames(only []string) ([]string, error) {
	if len(only) > 0 {
		for _, n := range only {
			if _, err := e.Cfg.App(n); err != nil {
				return nil, err
			}
		}
		return only, nil
	}
	var all []string
	for _, a := range e.Cfg.Apps {
		all = append(all, a.Name)
	}
	return all, nil
}

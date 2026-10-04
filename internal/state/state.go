// Package state persists what the scheduler needs between runs.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type App struct {
	LastSuccess time.Time `json:"last_success"`
	LastAttempt time.Time `json:"last_attempt"`
	LastError   string    `json:"last_error,omitempty"`
	LastBackup  string    `json:"last_backup,omitempty"`
	LastAlert   time.Time `json:"last_alert"`
	LastVerify  time.Time `json:"last_verify"`
}

type State struct {
	Apps           map[string]*App `json:"apps"`
	LastDrillNudge time.Time       `json:"last_drill_nudge"`
	path           string
}

func Load(dir string) (*State, error) {
	s := &State{Apps: map[string]*App{}, path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Apps == nil {
		s.Apps = map[string]*App{}
	}
	return s, nil
}

func (s *State) App(name string) *App {
	a, ok := s.Apps[name]
	if !ok {
		a = &App{}
		s.Apps[name] = a
	}
	return a
}

func (s *State) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

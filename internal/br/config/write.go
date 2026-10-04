package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Doc is the shape written by `hbr setup`. Field order is the order users see.
type Doc struct {
	Destination string          `yaml:"destination"`
	KeysDir     string          `yaml:"keys_dir,omitempty"`
	Schedule    Schedule        `yaml:"schedule"`
	Hosts       map[string]Host `yaml:"hosts,omitempty"`
	Retention   Retention       `yaml:"retention"`
	Verify      VerifyDoc       `yaml:"verify"`
	Apps        []AppDoc        `yaml:"apps"`
}

type VerifyDoc struct {
	ContainerCLI   string `yaml:"container_cli"`
	PostgresMajors []int  `yaml:"postgres_majors,flow"`
}

type AppDoc struct {
	Name    string      `yaml:"name"`
	Sources []SourceDoc `yaml:"sources"`
	Restore *RestoreDoc `yaml:"restore,omitempty"`
}

// SourceDoc covers the fields of the built-in source types.
type SourceDoc struct {
	Type      string `yaml:"type"`
	Name      string `yaml:"name"`
	Host      string `yaml:"host,omitempty"`
	Container string `yaml:"container,omitempty"`
	User      string `yaml:"user,omitempty"`
	Database  string `yaml:"database,omitempty"`
	URL       string `yaml:"url,omitempty"`
}

type RestoreDoc struct {
	Stop []StopSpec `yaml:"stop,omitempty"`
}

const header = `# hbr configuration — created by ` + "`hbr setup`" + `.
# Re-run ` + "`hbr setup`" + ` or edit by hand; validate with ` + "`hbr check`" + `.
# This file holds no secrets: database passwords (if any) live in the
# macOS Keychain, and the encryption keys live in keys_dir.

`

// Save writes doc to path atomically (0600), keeping any previous file as .prev.
func Save(path string, doc Doc) error {
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".prev", old, 0o600); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if _, err := Load(path); err != nil {
		return fmt.Errorf("written config does not validate: %w", err)
	}
	return nil
}

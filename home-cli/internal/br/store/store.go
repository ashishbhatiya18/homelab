// Package store manages encrypted backup files on disk:
//
//	<destination>/<app>/<app>-20261004T093000Z.tar.age
//	<destination>/<app>/<app>-20261004T093000Z.tar.age.sha256
//
// Files are written to a temporary name, synced, checksummed and only then
// renamed into place, so a half-written backup never looks like a real one.
package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	ext        = ".tar.age"
	sumExt     = ".sha256"
	timeLayout = "20060102T150405Z"
)

type Backup struct {
	App  string
	Path string
	Time time.Time
	Tag  string // "" for scheduled backups; e.g. "prerestore"
	Size int64
	Rows int64 // filled in for freshly written backups only
}

var nameRE = regexp.MustCompile(`^(.+)-(\d{8}T\d{6}Z)(?:-([a-z0-9]+))?\.tar\.age$`)

func fileName(app string, t time.Time, tag string) string {
	n := app + "-" + t.UTC().Format(timeLayout)
	if tag != "" {
		n += "-" + tag
	}
	return n + ext
}

// List returns an app's backups, newest first.
func List(dest, app string) ([]Backup, error) {
	dir := filepath.Join(dest, app)
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, e := range ents {
		m := nameRE.FindStringSubmatch(e.Name())
		if m == nil || m[1] != app {
			continue
		}
		t, err := time.Parse(timeLayout, m[2])
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{App: app, Path: filepath.Join(dir, e.Name()), Time: t, Tag: m[3], Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

// Find resolves a user-supplied file (path or bare name) or, if empty, the
// newest untagged backup.
func Find(dest, app, file string) (Backup, error) {
	all, err := List(dest, app)
	if err != nil {
		return Backup{}, err
	}
	for _, b := range all {
		if (file == "" && b.Tag == "") || (file != "" && (b.Path == file || filepath.Base(b.Path) == filepath.Base(file))) {
			return b, nil
		}
	}
	if file == "" {
		return Backup{}, fmt.Errorf("no backups for %s in %s", app, filepath.Join(dest, app))
	}
	return Backup{}, fmt.Errorf("backup %q not found for %s", file, app)
}

// Write creates a new backup by streaming content into it.
func Write(dest, app string, t time.Time, tag string, content func(io.Writer) error) (Backup, error) {
	dir := filepath.Join(dest, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Backup{}, err
	}
	final := filepath.Join(dir, fileName(app, t, tag))
	tmp := final + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return Backup{}, err
	}
	cleanup := func() { f.Close(); os.Remove(tmp) }
	h := sha256.New()
	bw := bufio.NewWriter(io.MultiWriter(f, h))
	if err := content(bw); err != nil {
		cleanup()
		return Backup{}, err
	}
	if err := bw.Flush(); err != nil {
		cleanup()
		return Backup{}, err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return Backup{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return Backup{}, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := os.WriteFile(final+sumExt, []byte(sum+"  "+filepath.Base(final)+"\n"), 0o600); err != nil {
		os.Remove(tmp)
		return Backup{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return Backup{}, err
	}
	info, err := os.Stat(final)
	if err != nil {
		return Backup{}, err
	}
	b := Backup{App: app, Path: final, Time: t.UTC().Truncate(time.Second), Tag: tag, Size: info.Size()}
	return b, VerifyChecksum(b)
}

// VerifyChecksum re-reads the file and compares it with its .sha256 sidecar.
func VerifyChecksum(b Backup) error {
	want, err := os.ReadFile(b.Path + sumExt)
	if err != nil {
		return fmt.Errorf("missing checksum for %s: %w", filepath.Base(b.Path), err)
	}
	f, err := os.Open(b.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.HasPrefix(string(want), got) {
		return fmt.Errorf("%s: checksum mismatch (file damaged)", filepath.Base(b.Path))
	}
	return nil
}

// Remove deletes a backup and its checksum.
func Remove(b Backup) error {
	if err := os.Remove(b.Path); err != nil {
		return err
	}
	os.Remove(b.Path + sumExt)
	return nil
}

// CleanPartials removes leftovers from interrupted writes.
func CleanPartials(dest, app string) {
	m, _ := filepath.Glob(filepath.Join(dest, app, "*.partial"))
	for _, p := range m {
		os.Remove(p)
	}
}

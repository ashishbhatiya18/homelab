// Package clean turns a finished download into just its payload: it removes
// junk files, unpacks archives, flattens single-file folders and moves the
// result out of the staging directory. It also powers `down clean`.
package clean

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Partial matches files that unfinished downloads leave behind.
var Partial = []string{"*.aria2", "*.part", "*.ytdl", "*.part-frag*", "*.temp", "*.temp.*"}

// StagingDir is where downloads happen before they're published; it lives
// in the output directory so publishing is a rename.
const StagingDir = ".down"

// Match reports whether name matches any pattern, case-insensitively.
func Match(name string, patterns []string) bool {
	name = strings.ToLower(name)
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), name); ok {
			return true
		}
	}
	return false
}

// IsJunk reports whether any component of a slash-separated relative path
// matches a junk pattern.
func IsJunk(rel string, junk []string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part != "" && Match(part, junk) {
			return true
		}
	}
	return false
}

type Action struct {
	Kind string // junk, partial, extract, staging
	Path string
	Size int64
}

type Options struct {
	Junk    []string
	Partial bool // also remove leftovers of unfinished downloads
	Extract bool
	DryRun  bool
}

// Tidy cleans dir in place: junk (and with Partial, unfinished-download
// leftovers) is removed, archives are unpacked and then removed, and empty
// directories are dropped. With DryRun nothing changes and the planned
// actions are returned.
func Tidy(dir string, o Options) ([]Action, error) {
	acts, err := removeMatching(dir, o)
	if err != nil || !o.Extract {
		if err == nil && !o.DryRun {
			removeEmptyDirs(dir)
		}
		return acts, err
	}
	ex, err := extractAll(dir, o.DryRun)
	acts = append(acts, ex...)
	if err != nil {
		return acts, err
	}
	if len(ex) > 0 && !o.DryRun {
		// Archives often carry their own junk.
		more, err := removeMatching(dir, o)
		acts = append(acts, more...)
		if err != nil {
			return acts, err
		}
	}
	if !o.DryRun {
		removeEmptyDirs(dir)
	}
	return acts, nil
}

// TidyTorrent cleans a torrent's staging dir: the saved metadata goes, and
// each top-level folder is tidied inside (its own name, e.g. "www.site -
// Movie", never makes it junk). A single-file torrent is kept as is, apart
// from being extracted when it's an archive.
func TidyTorrent(staging string, o Options) ([]Action, error) {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return nil, err
	}
	var acts []Action
	var errs []error
	for _, e := range entries {
		p := filepath.Join(staging, e.Name())
		if !e.IsDir() {
			if Match(e.Name(), append([]string{"*.torrent"}, Partial...)) {
				acts = append(acts, Action{Kind: "meta", Path: p, Size: size(p)})
				if !o.DryRun {
					os.Remove(p)
				}
			}
			continue
		}
		a, err := Tidy(p, o)
		acts = append(acts, a...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if o.Extract {
		a, err := extractAll(staging, o.DryRun)
		acts = append(acts, a...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return acts, errors.Join(errs...)
}

func removeMatching(dir string, o Options) ([]Action, error) {
	var acts []Action
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		name := d.Name()
		kind := ""
		switch {
		case d.IsDir() && name == StagingDir:
			if o.Partial {
				kind = "staging"
			} else {
				return fs.SkipDir
			}
		case Match(name, o.Junk):
			kind = "junk"
		case o.Partial && !d.IsDir() && Match(name, Partial):
			kind = "partial"
		}
		if kind == "" {
			return nil
		}
		acts = append(acts, Action{Kind: kind, Path: p, Size: size(p)})
		if !o.DryRun {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	return acts, err
}

func size(p string) int64 {
	var n int64
	filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func removeEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	// Deepest first, so parents empty out too.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		os.Remove(d) // fails (harmlessly) unless empty
	}
}

var (
	reRarPart = regexp.MustCompile(`(?i)^(.*)\.part(\d+)\.rar$`)
	reRarOld  = regexp.MustCompile(`(?i)\.r\d\d$`)
	reSplit   = regexp.MustCompile(`(?i)^(.*)\.(7z|zip)\.(\d+)$`)
	reArchive = regexp.MustCompile(`(?i)\.(zip|7z|rar)$`)
)

// Volumes reports whether name is the first volume of an archive and, if so,
// a pattern matching all of its volumes (for removal after extraction).
func Volumes(name string) (first bool, set *regexp.Regexp) {
	q := regexp.QuoteMeta
	if m := reRarPart.FindStringSubmatch(name); m != nil {
		if n, _ := strconv.Atoi(m[2]); n != 1 {
			return false, nil
		}
		return true, regexp.MustCompile(`(?i)^` + q(m[1]) + `\.part\d+\.rar$`)
	}
	if m := reSplit.FindStringSubmatch(name); m != nil {
		if n, _ := strconv.Atoi(m[3]); n != 1 {
			return false, nil
		}
		return true, regexp.MustCompile(`(?i)^` + q(m[1]+"."+m[2]) + `\.\d+$`)
	}
	if reRarOld.MatchString(name) {
		return false, nil
	}
	if reArchive.MatchString(name) {
		base := strings.TrimSuffix(name, filepath.Ext(name))
		if strings.EqualFold(filepath.Ext(name), ".rar") {
			return true, regexp.MustCompile(`(?i)^` + q(base) + `\.(rar|r\d\d)$`)
		}
		return true, regexp.MustCompile(`^` + q(name) + `$`)
	}
	return false, nil
}

// Extractor finds a tool that unpacks zip/rar/7z: 7-Zip, The Unarchiver's
// unar, else bsdtar (no multi-volume RAR). The returned func builds the
// command for archive → dir.
func Extractor() (string, func(archive, dir string) *exec.Cmd) {
	for _, bin := range []string{"7zz", "7z"} {
		if p, err := exec.LookPath(bin); err == nil {
			return bin, func(a, d string) *exec.Cmd { return exec.Command(p, "x", "-y", "-bso0", "-bsp0", "-o"+d, a) }
		}
	}
	if p, err := exec.LookPath("unar"); err == nil {
		return "unar", func(a, d string) *exec.Cmd { return exec.Command(p, "-q", "-f", "-D", "-o", d, a) }
	}
	if p, err := exec.LookPath("bsdtar"); err == nil {
		return "bsdtar", func(a, d string) *exec.Cmd { return exec.Command(p, "-xf", a, "-C", d) }
	}
	return "", nil
}

func extractAll(root string, dryRun bool) ([]Action, error) {
	var firsts []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if ok, _ := Volumes(d.Name()); ok {
				firsts = append(firsts, p)
			}
		}
		return nil
	})
	if len(firsts) == 0 {
		return nil, nil
	}
	tool, extract := Extractor()
	if extract == nil {
		return nil, errors.New("no archive tool found to extract (see down doctor)")
	}
	var acts []Action
	var failed []string
	for _, a := range firsts {
		dir, name := filepath.Split(a)
		_, set := Volumes(name)
		var vols []string
		entries, _ := os.ReadDir(dir)
		var total int64
		for _, e := range entries {
			if set.MatchString(e.Name()) {
				vols = append(vols, filepath.Join(dir, e.Name()))
				total += size(filepath.Join(dir, e.Name()))
			}
		}
		acts = append(acts, Action{Kind: "extract", Path: a, Size: total})
		if dryRun {
			continue
		}
		if out, err := extract(a, dir).CombinedOutput(); err != nil {
			// Keep the archive; the caller still publishes it.
			failed = append(failed, fmt.Sprintf("%s (%s: %s)", name, tool, strings.TrimSpace(string(out))))
			acts = acts[:len(acts)-1]
			continue
		}
		for _, v := range vols {
			os.Remove(v)
		}
	}
	if len(failed) > 0 {
		return acts, fmt.Errorf("could not extract %s", strings.Join(failed, ", "))
	}
	return acts, nil
}

// Payload lists the top-level entries of a staging directory that make up the
// download, skipping torrent metadata and aria2 control files.
func Payload(staging string, keepMeta bool) ([]string, error) {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !keepMeta && !e.IsDir() && Match(e.Name(), append([]string{"*.torrent"}, Partial...)) {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// Flat returns the single file inside a folder that holds nothing else
// (after cleanup), relative to staging, or "" if rel isn't such a folder.
func Flat(staging, rel string) string {
	var only string
	n := 0
	filepath.WalkDir(filepath.Join(staging, rel), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
			only = p
		}
		return nil
	})
	if n != 1 {
		return ""
	}
	r, _ := filepath.Rel(staging, only)
	if r == rel {
		return "" // already a file
	}
	return r
}

// Publish moves each relative path from staging into out (keeping its
// relative path, creating parent directories), renaming to "name (1)" etc.
// instead of overwriting. It returns the final paths.
func Publish(staging, out string, rels []string, flat bool) ([]string, error) {
	var done []string
	for _, rel := range rels {
		src := filepath.Join(staging, rel)
		dst := filepath.Join(out, rel)
		if flat {
			dst = filepath.Join(out, filepath.Base(rel))
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return done, err
		}
		dst = Unique(dst)
		if err := os.Rename(src, dst); err != nil {
			return done, err
		}
		done = append(done, dst)
	}
	return done, nil
}

// Unique returns p, or p with " (n)" before its extension if p exists.
func Unique(p string) string {
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		ext = ""
	}
	base := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		c := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(c); errors.Is(err, os.ErrNotExist) {
			return c
		}
	}
}

// Leftovers sums what is still in a staging directory after publishing: the
// metadata, partial files and junk that cleanup drops.
func Leftovers(staging string) (files int, bytes int64) {
	filepath.WalkDir(staging, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
			if fi, err := d.Info(); err == nil {
				bytes += fi.Size()
			}
		}
		return nil
	})
	return files, bytes
}

// RemoveStaging deletes a staging dir and its parent .down when empty.
func RemoveStaging(staging string) error {
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	os.Remove(filepath.Dir(staging))
	return nil
}

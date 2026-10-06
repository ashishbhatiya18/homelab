package store

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var agoRE = regexp.MustCompile(`^(\d+)\s*(d|day|days|w|week|weeks|m|mo|month|months)(\s+ago)?$`)

// Cutoff turns a human snapshot selector into a point in time. The chosen
// snapshot is the newest one taken at or before that point ("as of").
//
//	latest | now | today | yesterday
//	3 days ago | 2 weeks ago | 1 month ago | 3d | 2w | 1m
//	2026-10-03 | "2026-10-03 14:30"
func Cutoff(sel string, now time.Time) (time.Time, error) {
	s := strings.ToLower(strings.TrimSpace(sel))
	endOfDay := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 23, 59, 59, 0, t.Location())
	}
	switch s {
	case "", "latest", "now":
		return now, nil
	case "today":
		return endOfDay(now), nil
	case "yesterday":
		return endOfDay(now.AddDate(0, 0, -1)), nil
	}
	if m := agoRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2][0] {
		case 'd':
			return endOfDay(now.AddDate(0, 0, -n)), nil
		case 'w':
			return endOfDay(now.AddDate(0, 0, -7*n)), nil
		case 'm':
			return endOfDay(now.AddDate(0, -n, 0)), nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, now.Location()); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return endOfDay(t), nil
	}
	return time.Time{}, fmt.Errorf("unrecognised snapshot %q (try: latest, yesterday, \"3 days ago\", \"1 month ago\", 2026-10-03, or a file name)", sel)
}

// Resolve picks a backup by selector or by file name.
func Resolve(dest, app, sel string, now time.Time) (Backup, error) {
	all, err := List(dest, app)
	if err != nil {
		return Backup{}, err
	}
	if len(all) == 0 {
		return Backup{}, fmt.Errorf("no snapshots for %s in %s", app, filepath.Join(dest, app))
	}
	if strings.HasSuffix(sel, ext) {
		for _, b := range all {
			if b.Path == sel || filepath.Base(b.Path) == filepath.Base(sel) {
				return b, nil
			}
		}
		return Backup{}, fmt.Errorf("snapshot file %q not found for %s", sel, app)
	}
	cut, err := Cutoff(sel, now)
	if err != nil {
		return Backup{}, err
	}
	for _, b := range all { // newest first
		if b.Tag == "" && !b.Time.After(cut) {
			return b, nil
		}
	}
	oldest := all[len(all)-1]
	return Backup{}, fmt.Errorf("no snapshot of %s as of %s; the oldest is %s", app, cut.Format("2006-01-02 15:04"), oldest.Time.Local().Format("2006-01-02 15:04"))
}

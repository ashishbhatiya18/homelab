package store

import (
	"io"
	"testing"
	"time"
)

func TestResolveAsOf(t *testing.T) {
	dest := t.TempDir()
	loc := time.Local
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, loc)
	for _, d := range []int{0, 1, 2, 9, 34} { // today, yesterday, 2d, 9d, 34d ago at 02:00
		ts := time.Date(2026, 10, 4-d, 2, 0, 0, 0, loc)
		Write(dest, "app", ts, "", func(w io.Writer) error { return nil })
	}
	cases := map[string]string{
		"latest":      "2026-10-04",
		"yesterday":   "2026-10-03",
		"3 days ago":  "2026-09-25", // nothing on 10-01 → newest before it
		"1 week ago":  "2026-09-25",
		"1 month ago": "2026-08-31",
		"2026-10-02":  "2026-10-02",
		"2d":          "2026-10-02",
	}
	for sel, want := range cases {
		b, err := Resolve(dest, "app", sel, now)
		if err != nil {
			t.Errorf("%q: %v", sel, err)
			continue
		}
		if got := b.Time.In(loc).Format("2006-01-02"); got != want {
			t.Errorf("%q: got %s want %s", sel, got, want)
		}
	}
	if _, err := Resolve(dest, "app", "1 year ago", now); err == nil {
		t.Error("expected error for a date before the oldest snapshot")
	}
	if _, err := Resolve(dest, "app", "someday", now); err == nil {
		t.Error("expected error for nonsense selector")
	}
}

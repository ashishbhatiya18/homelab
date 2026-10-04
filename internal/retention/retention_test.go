package retention

import (
	"testing"
	"time"
)

func TestKeepDailyWeeklyMonthly(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, loc)
	var times []time.Time
	for d := 0; d < 120; d++ { // one backup a day for ~4 months
		times = append(times, now.AddDate(0, 0, -d))
	}
	keep := Keep(times, Policy{Daily: 7, Weekly: 4, Monthly: 12, MinKeep: 3}, loc)

	has := func(daysAgo int) bool { return keep[daysAgo] }
	for d := 0; d < 7; d++ {
		if !has(d) {
			t.Errorf("daily backup %d days ago should be kept", d)
		}
	}
	// A copy roughly a month old must always exist (the user's requirement).
	found := false
	for i := range times {
		age := now.Sub(times[i])
		if keep[i] && age >= 28*24*time.Hour && age <= 62*24*time.Hour {
			found = true
		}
	}
	if !found {
		t.Error("no kept backup between 28 and 62 days old")
	}
	if len(keep) > 7+4+12+3 {
		t.Errorf("kept %d backups, more than the policy allows", len(keep))
	}
	if len(keep) == len(times) {
		t.Error("nothing was pruned")
	}
}

func TestMinKeepWithFewBackups(t *testing.T) {
	now := time.Now()
	times := []time.Time{now, now.Add(-time.Hour), now.Add(-2 * time.Hour)}
	keep := Keep(times, Policy{Daily: 1, MinKeep: 3}, time.UTC)
	if len(keep) != 3 {
		t.Fatalf("expected all 3 kept, got %d", len(keep))
	}
}

func TestMonthOldCopyEveryDayOfMonth(t *testing.T) {
	loc := time.UTC
	for day := 1; day <= 31; day++ {
		now := time.Date(2026, 10, day, 9, 0, 0, 0, loc)
		var times []time.Time
		for d := 0; d < 90; d++ {
			times = append(times, now.AddDate(0, 0, -d))
		}
		keep := Keep(times, Policy{Daily: 7, Weekly: 4, Monthly: 12, MinKeep: 3}, loc)
		ok := false
		for i := range times {
			if keep[i] && now.Sub(times[i]) >= 28*24*time.Hour {
				ok = true
			}
		}
		if !ok {
			t.Errorf("on Oct %d no backup at least 28 days old is kept", day)
		}
	}
}

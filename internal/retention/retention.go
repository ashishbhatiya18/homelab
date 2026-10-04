// Package retention decides which backups to keep.
//
// Policy (grandfather-father-son):
//   - Daily:   the newest backup of each of the last N days that have backups.
//   - Weekly:  the oldest backup of each of the last N ISO weeks.
//   - Monthly: the oldest backup of each of the last N months.
//   - MinKeep: the N newest backups are always kept.
//
// Keeping the *oldest* of each week and month guarantees there is always a
// copy that is roughly a week and roughly a month old, not just recent ones.
package retention

import (
	"fmt"
	"sort"
	"time"
)

type Policy struct {
	Daily, Weekly, Monthly, MinKeep int
}

// Keep returns the indexes of times to keep. Times may be in any order.
func Keep(times []time.Time, p Policy, loc *time.Location) map[int]bool {
	idx := make([]int, len(times))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return times[idx[a]].After(times[idx[b]]) }) // newest first
	keep := map[int]bool{}
	for i := 0; i < p.MinKeep && i < len(idx); i++ {
		keep[idx[i]] = true
	}

	day := func(t time.Time) string { return t.In(loc).Format("2006-01-02") }
	week := func(t time.Time) string { y, w := t.In(loc).ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) }
	month := func(t time.Time) string { return t.In(loc).Format("2006-01") }

	// Daily: newest per bucket → iterate newest first, first seen wins.
	pick(idx, times, day, p.Daily, keep)
	// Weekly / monthly: oldest per bucket → iterate oldest first, restricted
	// to the N most recent buckets.
	oldestFirst := make([]int, len(idx))
	for i := range idx {
		oldestFirst[i] = idx[len(idx)-1-i]
	}
	pickRecentBuckets(oldestFirst, times, week, p.Weekly, keep)
	pickRecentBuckets(oldestFirst, times, month, p.Monthly, keep)
	return keep
}

func pick(order []int, times []time.Time, key func(time.Time) string, n int, keep map[int]bool) {
	seen := map[string]bool{}
	for _, i := range order {
		if len(seen) >= n {
			return
		}
		k := key(times[i])
		if !seen[k] {
			seen[k] = true
			keep[i] = true
		}
	}
}

func pickRecentBuckets(oldestFirst []int, times []time.Time, key func(time.Time) string, n int, keep map[int]bool) {
	if n <= 0 {
		return
	}
	var buckets []string
	first := map[string]int{}
	for _, i := range oldestFirst {
		k := key(times[i])
		if _, ok := first[k]; !ok {
			first[k] = i
			buckets = append(buckets, k)
		}
	}
	if len(buckets) > n {
		buckets = buckets[len(buckets)-n:]
	}
	for _, b := range buckets {
		keep[first[b]] = true
	}
}

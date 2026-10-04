package node

import "time"

func parseDockerTime(s string) (time.Time, error) {
	return time.Parse("2006-01-02 15:04:05 -0700 MST", s)
}

func daysSince(t time.Time) int { return int(time.Since(t).Hours() / 24) }

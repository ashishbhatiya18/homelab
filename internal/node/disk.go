package node

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/ashishbhatiya18/home/internal/remote"
)

// DiskReport explains where a node's disk space goes and what is safe to
// reclaim. Nothing in use is ever offered for removal: images used by any
// container (running or stopped) and volumes are left alone, and rollback
// images kept by `home stack update` are only offered once they are old.
type DiskReport struct {
	Size, Used, Avail int64
	Percent           int
	Docker            []string // `docker system df` rows
	Dirs              []Dir    // largest directories on the root filesystem
	Cleanups          []Cleanup
}

// Dir is a directory and its size (report only; never offered for deletion).
type Dir struct {
	Path  string
	Bytes int64
}

// Cleanup is one optional step with its estimated gain.
type Cleanup struct {
	Name, Detail string
	Bytes        int64
	cmd          string
}

const diskScript = `
df -B1 --output=size,used,avail,pcent / | tail -1 | awk '{gsub("%","",$4); print "df="$1"|"$2"|"$3"|"$4}'
docker system df --format 'sysdf={{.Type}}|{{.Size}}|{{.Reclaimable}}'
docker images -f dangling=true --format 'dangling={{.ID}}|{{.Size}}'
used=$(docker ps -aq | xargs -r docker inspect --format '{{.Image}}' | sort -u)
docker images --no-trunc --filter dangling=false --format '{{.ID}}|{{.Repository}}:{{.Tag}}|{{.Size}}|{{.CreatedAt}}' | while IFS='|' read -r id ref size created; do
  echo "$used" | grep -qx "$id" && continue
  case "$ref" in home-rollback/*) echo "rollback=$id|$ref|$size|$created" ;; *) echo "unused=$id|$ref|$size|$created" ;; esac
done
docker system df --format '{{.Type}}|{{.Reclaimable}}' | awk -F'|' '$1=="Build Cache"{print "buildcache="$2}'
sudo -n find /var/lib/docker/containers -name '*-json.log' -size +50M -printf 'log=%s|%p\n' 2>/dev/null
echo "journal=$(sudo -n journalctl --disk-usage 2>/dev/null | grep -oE '[0-9.]+[KMGT]?B?' | head -1)"
echo "aptcache=$(sudo -n du -sb /var/cache/apt/archives 2>/dev/null | cut -f1)"
sudo -n timeout 120 du -x -B1 -d 5 / 2>/dev/null | sort -rn | awk 'NR>1{print "dir="$1"|"$2}' | head -40
`

// Disk gathers the report. Rollback images older than rollbackDays are offered.
func (c *Client) Disk(ctx context.Context, rollbackDays int) (*DiskReport, error) {
	out, err := c.R.Output(ctx, c.Name, "bash -s <<'HOME_EOF'\n"+diskScript+"HOME_EOF")
	if err != nil {
		return nil, err
	}
	r := &DiskReport{}
	var dangling, unused, rollback []string
	var danglingB, unusedB, rollbackB, logB int64
	var logs []string
	names, _ := c.containerNames(ctx)
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		f := strings.Split(v, "|")
		switch k {
		case "df":
			if len(f) == 4 {
				r.Size, _ = strconv.ParseInt(f[0], 10, 64)
				r.Used, _ = strconv.ParseInt(f[1], 10, 64)
				r.Avail, _ = strconv.ParseInt(f[2], 10, 64)
				r.Percent, _ = strconv.Atoi(f[3])
			}
		case "sysdf":
			if len(f) == 3 {
				r.Docker = append(r.Docker, fmt.Sprintf("%-14s %9s   reclaimable %s", f[0], f[1], f[2]))
			}
		case "dangling":
			dangling = append(dangling, f[0])
			danglingB += ParseSize(f[len(f)-1])
		case "unused":
			if len(f) == 4 {
				unused = append(unused, f[0])
				unusedB += ParseSize(f[2])
			}
		case "rollback":
			if len(f) == 4 && olderThan(f[3], rollbackDays) {
				rollback = append(rollback, f[1])
				rollbackB += ParseSize(f[2])
			}
		case "buildcache":
			if b := ParseSize(strings.Fields(v + " ")[0]); b > 0 {
				r.Cleanups = append(r.Cleanups, Cleanup{Name: "Docker build cache", Detail: "rebuilt automatically when needed", Bytes: b, cmd: "docker builder prune -af"})
			}
		case "log":
			if len(f) == 2 {
				n, _ := strconv.ParseInt(f[0], 10, 64)
				logB += n
				logs = append(logs, f[1])
			}
		case "journal":
			if b := ParseSize(v); b > 300<<20 {
				r.Cleanups = append(r.Cleanups, Cleanup{Name: "System journal", Detail: "shrink to 200 MB (oldest entries go)", Bytes: b - 200<<20, cmd: "sudo -n journalctl --vacuum-size=200M"})
			}
		case "dir":
			if len(f) == 2 {
				b, _ := strconv.ParseInt(f[0], 10, 64)
				r.Dirs = append(r.Dirs, Dir{Path: f[1], Bytes: b})
			}
		case "aptcache":
			if b, _ := strconv.ParseInt(v, 10, 64); b > 50<<20 {
				r.Cleanups = append(r.Cleanups, Cleanup{Name: "apt package cache", Detail: "downloaded .deb files", Bytes: b, cmd: "sudo -n apt-get clean"})
			}
		}
	}
	if len(dangling) > 0 {
		r.Cleanups = append(r.Cleanups, Cleanup{Name: "Dangling images", Detail: fmt.Sprintf("%d untagged leftovers of old pulls/builds", len(dangling)), Bytes: danglingB, cmd: "docker image prune -f"})
	}
	if len(unused) > 0 {
		r.Cleanups = append(r.Cleanups, Cleanup{Name: "Unused images", Detail: fmt.Sprintf("%d images no container uses (re-pulled if needed)", len(unused)), Bytes: unusedB,
			cmd: "docker rmi " + quoteAll(unused) + " 2>/dev/null; true"})
	}
	if len(rollback) > 0 {
		r.Cleanups = append(r.Cleanups, Cleanup{Name: "Old rollback images", Detail: fmt.Sprintf("%d kept by `home stack update`, older than %d days", len(rollback), rollbackDays), Bytes: rollbackB,
			cmd: "docker rmi " + quoteAll(rollback) + " 2>/dev/null; true"})
	}
	if len(logs) > 0 {
		var named []string
		for _, l := range logs {
			id := strings.Split(strings.TrimPrefix(l, "/var/lib/docker/containers/"), "/")[0]
			if n := names[id]; n != "" {
				named = append(named, n)
			}
		}
		sort.Strings(named)
		r.Cleanups = append(r.Cleanups, Cleanup{Name: "Large container logs", Detail: "empty logs over 50 MB: " + strings.Join(named, ", ") + " (history is lost)", Bytes: logB,
			cmd: "sudo -n truncate -s 0 " + quoteAll(logs)})
	}
	sort.SliceStable(r.Cleanups, func(i, j int) bool { return r.Cleanups[i].Bytes > r.Cleanups[j].Bytes })
	r.Dirs = leaves(r.Dirs, r.Used/50)
	return r, nil
}

// Clean runs the chosen cleanups.
func (c *Client) Clean(ctx context.Context, steps []Cleanup, out io.Writer) error {
	for _, s := range steps {
		fmt.Fprintf(out, "• %s …\n", s.Name)
		if err := c.R.Run(ctx, c.Name, s.cmd, nil, io.Discard); err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return nil
}

func (c *Client) containerNames(ctx context.Context) (map[string]string, error) {
	out, err := c.R.Output(ctx, c.Name, "docker ps -a --no-trunc --format '{{.ID}} {{.Names}}'")
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if id, n, ok := strings.Cut(l, " "); ok {
			m[id] = n
		}
	}
	return m, err
}

func quoteAll(s []string) string {
	q := make([]string, len(s))
	for i, v := range s {
		q[i] = remote.Quote(v)
	}
	return strings.Join(q, " ")
}

// ParseSize reads Docker/journalctl sizes like "1.2GB", "512MB", "3.4G", "120kB".
func ParseSize(s string) int64 {
	s = strings.TrimSpace(strings.Split(s, " ")[0])
	if s == "" {
		return 0
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(strings.TrimSuffix(strings.TrimSuffix(s[i:], "iB"), "B")) {
	case "K":
		n *= 1 << 10
	case "M":
		n *= 1 << 20
	case "G":
		n *= 1 << 30
	case "T":
		n *= 1 << 40
	}
	return int64(n)
}

// olderThan parses Docker's CreatedAt ("2026-05-11 21:38:23 +0000 UTC").
func olderThan(createdAt string, days int) bool {
	t, err := parseDockerTime(createdAt)
	return err == nil && daysSince(t) > days
}

// Human formats bytes.
func Human(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%d KB", b>>10)
}

// leaves keeps the most specific large directories: a parent is dropped when
// a child accounts for most of it, so the list points at the real culprits.
func leaves(dirs []Dir, min int64) []Dir {
	var big []Dir
	for _, d := range dirs {
		if d.Bytes >= min && d.Path != "/" {
			big = append(big, d)
		}
	}
	var out []Dir
	for _, d := range big {
		covered := false
		for _, c := range big {
			if c.Path != d.Path && strings.HasPrefix(c.Path, d.Path+"/") && c.Bytes*10 >= d.Bytes*7 {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

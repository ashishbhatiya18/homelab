// Package immich shrinks the videos an Immich server stores. Each untreated
// original is copied to the Mac, re-encoded with x265 (HDR, audio and
// metadata kept), verified by a full decode, and swapped in place on the
// node; then Immich's record is updated.
//
// Immich's checksum of the asset is deliberately left as the original's:
// phones dedupe their backups by it, so a new checksum would make them upload
// the original again. A file whose bytes no longer match that checksum, or
// that carries the Marker comment, is therefore already treated.
package immich

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ashishbhatiya18/home/internal/homecfg"
	"github.com/ashishbhatiya18/home/internal/remote"
)

// Marker is the comment tag written into every treated video.
const Marker = "home-compress"

// Asset is one video as Immich records it.
type Asset struct {
	ID, Checksum, Path, Owner string // Path as Immich sees it (/data/…)
	Size, DurationMS          int64
}

// Mbps is the bitrate from Immich's record; 0 when unknown.
func (a Asset) Mbps() float64 {
	if a.Size <= 0 || a.DurationMS <= 0 {
		return 0
	}
	return float64(a.Size*8) / float64(a.DurationMS) / 1000
}

// Entry is what happened to an asset; treated, skipped and failed assets
// are not tried again (delete the state file entry to retry one).
type Entry struct {
	Result string    `json:"result"` // done, skipped, failed
	Note   string    `json:"note,omitempty"`
	Path   string    `json:"path,omitempty"`
	From   int64     `json:"from,omitempty"`
	To     int64     `json:"to,omitempty"`
	At     time.Time `json:"at"`
}

type State map[string]Entry

func LoadState(p string) State {
	s := State{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (s State) Save(p string) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Summary of one run.
type Summary struct {
	Done, Skipped, Left int
	Saved               int64
	Failed              []string
	Stopped             string // why the run ended early, if it did
	Resume              bool   // ended early for a passing reason: run again soon
}

func (s Summary) String() string {
	msg := fmt.Sprintf("%d treated (saved %s), %d skipped, %d failed", s.Done, Human(s.Saved), s.Skipped, len(s.Failed))
	if s.Left > 0 {
		msg += fmt.Sprintf(", %d left", s.Left)
	}
	if s.Stopped != "" {
		msg += " — stopped: " + s.Stopped
	}
	return msg
}

type Runner struct {
	R         *remote.Runner
	Cfg       homecfg.Immich // Resolved
	StatePath string
	Logf      func(string, ...any)
	// Pause, when set, is asked before each video and every minute while
	// encoding; a reason ("on battery") stops the run there, and the video
	// being encoded is tried again next run.
	Pause func() string
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// sql runs q with psql in the Postgres container; rows are \x1f-separated.
func (x *Runner) sql(ctx context.Context, q string) (string, error) {
	c := x.Cfg
	cmd := fmt.Sprintf(`docker exec -i %s psql -X -q -v ON_ERROR_STOP=1 -U %s -d %s -tA -F "$(printf '\037')"`,
		remote.Quote(c.DBContainer), remote.Quote(c.DBUser), remote.Quote(c.DBName))
	var out bytes.Buffer
	err := x.R.Run(ctx, c.DBNode, cmd, strings.NewReader(q), &out)
	return out.String(), err
}

// candidatesSQL: live, library-owned videos Immich can play from a file we
// can rewrite. Live Photo motion clips (hidden) are tiny and skipped; assets
// uploaded in the last day may still be processed by Immich's own jobs.
const candidatesSQL = `select a.id, encode(a.checksum,'hex'), a."originalPath", coalesce(e."fileSizeInByte",0), coalesce(a.duration,0), u.email
from asset a join "user" u on u.id=a."ownerId" left join asset_exif e on e."assetId"=a.id
where a.type='VIDEO' and a.status='active' and a."deletedAt" is null and a.visibility <> 'hidden'
  and a."libraryId" is null and not a."isOffline" and a."createdAt" < now() - interval '1 day'
  and a."originalPath" like '/data/%' and lower(a."originalPath") ~ '\.(mov|mp4|m4v)$'
order by e."fileSizeInByte" desc nulls last;`

// Candidates lists the videos Immich has that could be treated.
func (x *Runner) Candidates(ctx context.Context) ([]Asset, error) {
	out, err := x.sql(ctx, candidatesSQL)
	if err != nil {
		return nil, err
	}
	return parseAssets(out)
}

func parseAssets(out string) ([]Asset, error) {
	var as []Asset
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) != 6 || !uuidRe.MatchString(f[0]) {
			return nil, fmt.Errorf("unexpected row from Immich: %q", line)
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		dur, _ := strconv.ParseInt(f[4], 10, 64)
		as = append(as, Asset{ID: f[0], Checksum: f[1], Path: f[2], Size: size, DurationMS: dur, Owner: f[5]})
	}
	return as, nil
}

// hostPath maps Immich's /data/… path to the file on the node.
func (x *Runner) hostPath(p string) (string, error) {
	rel, ok := strings.CutPrefix(path.Clean(p), "/data/")
	if !ok {
		return "", fmt.Errorf("path outside /data: %s", p)
	}
	return x.Cfg.FilesDir + "/" + rel, nil
}

// Run treats every untreated candidate (at most limit when > 0). With dry it
// only lists what it would try.
func (x *Runner) Run(ctx context.Context, limit int, dry bool) (Summary, error) {
	var sum Summary
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			return sum, fmt.Errorf("%s not found (brew install ffmpeg)", tool)
		}
	}
	assets, err := x.Candidates(ctx)
	if err != nil {
		return sum, err
	}
	st := LoadState(x.StatePath)
	var todo []Asset
	for _, a := range assets {
		if _, seen := st[a.ID]; seen {
			continue
		}
		if m := a.Mbps(); m > 0 && m < x.Cfg.MinMbps {
			if !dry {
				st[a.ID] = Entry{Result: "skipped", Note: fmt.Sprintf("%.1f Mbps", m), Path: a.Path, At: time.Now()}
			}
			sum.Skipped++
			continue
		}
		todo = append(todo, a)
	}
	if dry {
		var total int64
		for _, a := range todo {
			total += a.Size
			x.Logf("%8s  %5.1f Mbps  %s  %s", Human(a.Size), a.Mbps(), a.Owner, a.Path)
		}
		sum.Left = len(todo)
		x.Logf("%d videos to treat (%s), %d skipped as already small", len(todo), Human(total), sum.Skipped)
		return sum, nil
	}
	if err := st.Save(x.StatePath); err != nil {
		return sum, err
	}
	if err := x.housekeep(ctx); err != nil {
		return sum, err
	}
	dir, err := os.MkdirTemp("", "home-immich-")
	if err != nil {
		return sum, err
	}
	defer os.RemoveAll(dir)

	for i, a := range todo {
		if limit > 0 && i >= limit {
			sum.Left = len(todo) - i
			break
		}
		if x.Pause != nil {
			if why := x.Pause(); why != "" {
				sum.Left, sum.Stopped, sum.Resume = len(todo)-i, why, true
				break
			}
		}
		if free, err := x.freeGB(ctx); err != nil || free < float64(x.Cfg.MinFreeGB) {
			sum.Left = len(todo) - i
			sum.Stopped = fmt.Sprintf("%s has %.0f GB free, under min_free_gb %d (originals are kept %d days)", x.Cfg.FilesNode, free, x.Cfg.MinFreeGB, *x.Cfg.KeepDays)
			if err != nil {
				sum.Stopped = err.Error()
			}
			break
		}
		x.Logf("[%d/%d] %s (%s, %s)", i+1, len(todo), a.Path, Human(a.Size), a.Owner)
		e := x.one(ctx, a, dir)
		if ctx.Err() != nil || e.Result == "paused" { // not the video's fault: try it next time
			sum.Left, sum.Stopped, sum.Resume = len(todo)-i, "interrupted", true
			if e.Result == "paused" {
				sum.Stopped = e.Note
			}
			break
		}
		e.Path, e.At = a.Path, time.Now()
		st[a.ID] = e
		if err := st.Save(x.StatePath); err != nil {
			return sum, err
		}
		switch e.Result {
		case "done":
			sum.Done++
			sum.Saved += e.From - e.To
			x.Logf("  ✓ %s → %s", Human(e.From), Human(e.To))
		case "skipped":
			sum.Skipped++
			x.Logf("  – skipped: %s", e.Note)
		default:
			sum.Failed = append(sum.Failed, a.Path+": "+e.Note)
			x.Logf("  ✗ %s", e.Note)
		}
	}
	return sum, nil
}

func failed(err error) Entry { return Entry{Result: "failed", Note: err.Error()} }

// one treats a single asset in the scratch directory dir.
func (x *Runner) one(ctx context.Context, a Asset, dir string) Entry {
	host, err := x.hostPath(a.Path)
	if err != nil {
		return failed(err)
	}
	ext := strings.ToLower(path.Ext(a.Path))
	src, out := filepath.Join(dir, "in"+ext), filepath.Join(dir, "out"+ext)
	defer os.Remove(src)
	defer os.Remove(out)

	// 1. Fetch the original and check it is the file Immich knows.
	f, err := os.Create(src)
	if err != nil {
		return failed(err)
	}
	h := sha1.New()
	err = x.R.Run(ctx, x.Cfg.FilesNode, "cat "+remote.Quote(host), nil, io.MultiWriter(f, h))
	f.Close()
	if err != nil {
		return failed(err)
	}
	if hex.EncodeToString(h.Sum(nil)) != a.Checksum {
		return Entry{Result: "skipped", Note: "file differs from Immich's checksum (already treated, or changed)"}
	}

	// 2. Decide from the file itself.
	in, err := Probe(ctx, src)
	if err != nil {
		return failed(fmt.Errorf("probe original: %w", err))
	}
	if in.Comment == Marker {
		return Entry{Result: "skipped", Note: "already treated"}
	}
	if !in.Video {
		return Entry{Result: "skipped", Note: "no video stream"}
	}
	if in.Mbps < x.Cfg.MinMbps {
		return Entry{Result: "skipped", Note: fmt.Sprintf("%.1f Mbps", in.Mbps)}
	}

	// 3. Encode, then confirm the result plays and matches.
	// Only the encode is aborted by Pause; the swap below never is.
	ectx, cancel := context.WithCancel(ctx)
	defer cancel()
	paused := make(chan string, 1)
	if x.Pause != nil {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ectx.Done():
					return
				case <-t.C:
					if why := x.Pause(); why != "" {
						paused <- why
						cancel()
						return
					}
				}
			}
		}()
	}
	enc := exec.CommandContext(ectx, "nice", append([]string{"-n", "10", "ffmpeg"}, EncodeArgs(src, out, in, x.Cfg.CRF, x.Cfg.Preset)...)...)
	b, err := enc.CombinedOutput()
	cancel()
	select {
	case why := <-paused:
		return Entry{Result: "paused", Note: why}
	default:
	}
	if err != nil {
		return failed(fmt.Errorf("encode: %v: %s", err, lastLine(b)))
	}
	res, err := Probe(ctx, out)
	if err != nil {
		return failed(fmt.Errorf("probe result: %w", err))
	}
	if err := Verify(in, res); err != nil {
		return failed(err)
	}
	if err := decodes(ctx, out); err != nil {
		return failed(err)
	}
	if float64(res.Size) > float64(in.Size)*(1-x.Cfg.MinSaving) {
		return Entry{Result: "skipped", Note: fmt.Sprintf("re-encode saves only %.0f%%", 100*(1-float64(res.Size)/float64(in.Size)))}
	}

	// 4. Upload next to the original and swap it in.
	if err := x.swap(ctx, a, host, out, res.Size); err != nil {
		return failed(err)
	}

	// 5. Tell Immich: new size, and a touched asset row (new updateId) so
	// web and mobile clients re-sync it. The checksum stays (see package doc).
	q := fmt.Sprintf(`begin;
update asset_exif set "fileSizeInByte"=%d where "assetId"='%s';
update asset set "updatedAt"=now() where id='%s';
commit;`, res.Size, a.ID, a.ID)
	if _, err := x.sql(ctx, q); err != nil {
		return Entry{Result: "done", Note: "replaced, but updating Immich failed: " + err.Error(), From: in.Size, To: res.Size}
	}
	return Entry{Result: "done", From: in.Size, To: res.Size}
}

// swap uploads file to <host>.home-compress.tmp and replaces host with it,
// after checking the upload is complete and the original is unchanged. The
// original moves to OriginalsDir/<asset id><ext> (or is deleted when
// keep_days is 0); restore.tsv there records where each one came from.
func (x *Runner) swap(ctx context.Context, a Asset, host, file string, size int64) error {
	tmp := host + ".home-compress.tmp"
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	err = x.R.Run(ctx, x.Cfg.FilesNode, "cat > "+remote.Quote(tmp), f, nil)
	f.Close()
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	keep, odir := "", x.Cfg.OriginalsDir
	if *x.Cfg.KeepDays > 0 {
		keep = odir + "/" + a.ID + strings.ToLower(path.Ext(host))
	}
	script := fmt.Sprintf(`set -e
p=%s; t=%s; o=%s; d=%s
[ "$(stat -c %%s "$t")" = %d ] || { rm -f "$t"; echo "upload incomplete" >&2; exit 1; }
[ "$(sha1sum < "$p" | cut -d' ' -f1)" = %s ] || { rm -f "$t"; echo "original changed meanwhile" >&2; exit 1; }
chmod --reference="$p" "$t"; touch -r "$p" "$t"
if [ -n "$o" ]; then
  mkdir -p "$d"; mv "$p" "$o"; touch "$o"
  printf '%%s\t%%s\t%%s\n' "$(date -Is)" "$o" "$p" >> "$d/restore.tsv"
  mv "$t" "$p" || { mv "$o" "$p"; exit 1; }
else
  mv "$t" "$p"
fi`, remote.Quote(host), remote.Quote(tmp), remote.Quote(keep), remote.Quote(odir), size, a.Checksum)
	if err := x.R.Run(ctx, x.Cfg.FilesNode, script, nil, nil); err != nil {
		return fmt.Errorf("replace: %w", err)
	}
	return nil
}

// housekeep deletes originals kept longer than keep_days and uploads left
// behind by an interrupted run.
func (x *Runner) housekeep(ctx context.Context) error {
	c := x.Cfg
	cmd := fmt.Sprintf(`find %s -name '*.home-compress.tmp' -mmin +120 -delete 2>/dev/null; `, remote.Quote(c.FilesDir))
	cmd += fmt.Sprintf(`[ -d %[1]s ] && find %[1]s -type f ! -name restore.tsv -mtime +%[2]d -delete; true`, remote.Quote(c.OriginalsDir), max(*c.KeepDays-1, 0))
	return x.R.Run(ctx, c.FilesNode, cmd, nil, nil)
}

func (x *Runner) freeGB(ctx context.Context) (float64, error) {
	out, err := x.R.Output(ctx, x.Cfg.FilesNode, "df -Pk "+remote.Quote(x.Cfg.FilesDir)+" | awk 'NR==2{print $4}'")
	if err != nil {
		return 0, err
	}
	kb, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil {
		return 0, fmt.Errorf("free space of %s: %q", x.Cfg.FilesDir, out)
	}
	return kb / 1024 / 1024, nil
}

// Media is what ffprobe says about a file.
type Media struct {
	Video, Audio bool
	PixFmt       string
	Duration     float64 // seconds
	Size         int64
	Mbps         float64
	Comment      string
}

func Probe(ctx context.Context, file string) (Media, error) {
	b, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-print_format", "json",
		"-show_entries", "stream=codec_type,pix_fmt:format=duration,size,bit_rate:format_tags=comment", file).Output()
	if err != nil {
		return Media{}, err
	}
	return parseProbe(b)
}

func parseProbe(b []byte) (Media, error) {
	var p struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			PixFmt    string `json:"pix_fmt"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Size     string            `json:"size"`
			BitRate  string            `json:"bit_rate"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return Media{}, err
	}
	var m Media
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			if !m.Video {
				m.Video, m.PixFmt = true, s.PixFmt
			}
		case "audio":
			m.Audio = true
		}
	}
	m.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	m.Size, _ = strconv.ParseInt(p.Format.Size, 10, 64)
	br, _ := strconv.ParseFloat(p.Format.BitRate, 64)
	m.Mbps = br / 1e6
	m.Comment = p.Format.Tags["comment"]
	return m, nil
}

// EncodeArgs re-encodes the first video stream with x265 at the source's bit
// depth; color tags (HDR), audio and container metadata (date, location,
// camera) carry over, and the Marker comment is added.
func EncodeArgs(in, out string, src Media, crf int, preset string) []string {
	pix := "yuv420p"
	if strings.Contains(src.PixFmt, "10") {
		pix = "yuv420p10le"
	}
	return []string{"-nostdin", "-v", "error", "-y", "-i", in,
		"-map", "0:v:0", "-map", "0:a?", "-map_metadata", "0",
		"-c:v", "libx265", "-preset", preset, "-crf", strconv.Itoa(crf), "-pix_fmt", pix,
		"-tag:v", "hvc1", "-x265-params", "log-level=error",
		"-c:a", "copy", "-movflags", "use_metadata_tags+faststart",
		"-metadata", "comment=" + Marker, out}
}

// Verify checks the result has the original's streams and length.
func Verify(in, out Media) error {
	switch {
	case !out.Video:
		return errors.New("result has no video stream")
	case in.Audio && !out.Audio:
		return errors.New("result lost the audio")
	case out.Comment != Marker:
		return errors.New("result lacks the marker comment")
	case math.Abs(in.Duration-out.Duration) > math.Max(1, in.Duration*0.01):
		return fmt.Errorf("result is %.1fs, original %.1fs", out.Duration, in.Duration)
	}
	return nil
}

// decodes plays the whole file through the decoder; any error fails it.
func decodes(ctx context.Context, file string) error {
	b, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-i", file, "-f", "null", "-").CombinedOutput()
	if err != nil || len(bytes.TrimSpace(b)) > 0 {
		return fmt.Errorf("result does not decode cleanly: %v %s", err, lastLine(b))
	}
	return nil
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func Human(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB"} {
		if math.Abs(f) < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1f TB", f)
}

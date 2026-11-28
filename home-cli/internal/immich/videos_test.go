package immich

import (
	"slices"
	"strings"
	"testing"

	"github.com/ashishbhatiya18/home/internal/homecfg"
)

func TestParseAssets(t *testing.T) {
	out := "0b3c9a8e-1f2d-4c5b-9a7e-6d5c4b3a2f10\x1fb8506b0b\x1f/data/library/a/IMG 1.mov\x1f74397497\x1f64066\x1fa@b\n"
	as, err := parseAssets(out)
	if err != nil || len(as) != 1 {
		t.Fatalf("parseAssets = %v, %v", as, err)
	}
	a := as[0]
	if a.Path != "/data/library/a/IMG 1.mov" || a.Size != 74397497 || a.DurationMS != 64066 {
		t.Errorf("got %+v", a)
	}
	if m := a.Mbps(); m < 9.2 || m > 9.4 { // ffprobe says 9.29 for this clip
		t.Errorf("Mbps = %v", m)
	}
	if _, err := parseAssets("not-a-uuid\x1fx\x1fy\x1f1\x1f1\x1fz"); err == nil {
		t.Error("a malformed id must be refused (it ends up in SQL)")
	}
	if as, err := parseAssets("\n"); err != nil || len(as) != 0 {
		t.Errorf("empty output = %v, %v", as, err)
	}
}

func TestHostPath(t *testing.T) {
	x := &Runner{Cfg: homecfg.Immich{FilesDir: "/srv/immich"}}
	if p, err := x.hostPath("/data/library/a/b.mov"); err != nil || p != "/srv/immich/library/a/b.mov" {
		t.Errorf("hostPath = %q, %v", p, err)
	}
	for _, bad := range []string{"/elsewhere/b.mov", "/data/../etc/passwd"} {
		if _, err := x.hostPath(bad); err == nil {
			t.Errorf("hostPath(%q) must fail", bad)
		}
	}
}

func TestParseProbe(t *testing.T) {
	js := `{"streams":[{"codec_type":"video","pix_fmt":"yuv420p10le"},{"codec_type":"audio"},{"codec_type":"data"}],
"format":{"duration":"64.066667","size":"74397497","bit_rate":"9290009","tags":{"comment":"home-compress"}}}`
	m, err := parseProbe([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Video || !m.Audio || m.PixFmt != "yuv420p10le" || m.Size != 74397497 || m.Comment != Marker || m.Duration < 64 || m.Mbps < 9.2 {
		t.Errorf("got %+v", m)
	}
}

func TestVerify(t *testing.T) {
	in := Media{Video: true, Audio: true, Duration: 64}
	ok := Media{Video: true, Audio: true, Duration: 64.4, Comment: Marker}
	if err := Verify(in, ok); err != nil {
		t.Errorf("good result refused: %v", err)
	}
	bad := map[string]Media{
		"no video":  {Audio: true, Duration: 64, Comment: Marker},
		"no audio":  {Video: true, Duration: 64, Comment: Marker},
		"no marker": {Video: true, Audio: true, Duration: 64},
		"too short": {Video: true, Audio: true, Duration: 60, Comment: Marker},
	}
	for name, out := range bad {
		if Verify(in, out) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEncodeArgs(t *testing.T) {
	a := EncodeArgs("in.mov", "out.mov", Media{PixFmt: "yuv420p10le"}, 26, "medium")
	s := strings.Join(a, " ")
	for _, want := range []string{"-pix_fmt yuv420p10le", "-crf 26", "-c:a copy", "-map_metadata 0", "-tag:v hvc1", "comment=" + Marker} {
		if !strings.Contains(s, want) {
			t.Errorf("args lack %q: %s", want, s)
		}
	}
	if a[len(a)-1] != "out.mov" {
		t.Errorf("output must be last: %v", a)
	}
	if b := EncodeArgs("i", "o", Media{PixFmt: "yuv420p"}, 26, "medium"); !slices.Contains(b, "yuv420p") {
		t.Errorf("8-bit source must stay 8-bit: %v", b)
	}
}

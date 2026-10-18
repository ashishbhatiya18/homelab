package ytdl

import (
	"slices"
	"strings"
	"testing"
)

func TestParseLine(t *testing.T) {
	p, f, ok := ParseLine("[down] 1048576|4194304.0|524288.5|2|10|A | B")
	if !ok || f != "" || p.Done != 1<<20 || p.Total != 4<<20 || p.Speed != 524288 || p.Item != 2 || p.Items != 10 || p.Title != "A | B" {
		t.Fatalf("download line: %+v %q %v", p, f, ok)
	}
	p, _, _ = ParseLine("[down] 10|NA|NA|NA|NA|T")
	if p.Total != 0 || p.Speed != 0 || p.Items != 0 {
		t.Fatalf("NA fields: %+v", p)
	}
	if p, _, ok := ParseLine("[down-pp] ExtractAudio|T"); !ok || p.PostProcess != "ExtractAudio" {
		t.Fatalf("pp line: %+v", p)
	}
	if _, f, _ := ParseLine("[down-file] /x/T [id].mp3"); f != "/x/T [id].mp3" {
		t.Fatalf("file line: %q", f)
	}
	if _, _, ok := ParseLine("[youtube] something"); ok {
		t.Fatal("foreign line parsed")
	}
}

func TestArgs(t *testing.T) {
	a, err := Args("U", Options{Audio: true, AudioFormat: "m4a", Dir: "/d", Template: "T"})
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(a, " ")
	for _, want := range []string{"--extract-audio", "--audio-format m4a", "--no-playlist", "--paths /d", "-- U"} {
		if !strings.Contains(s, want) {
			t.Errorf("audio args lack %q: %s", want, s)
		}
	}
	a, _ = Args("U", Options{Quality: "4k", Container: "mp4", Subs: "en", Playlist: true, Items: "1-3"})
	s = strings.Join(a, " ")
	for _, want := range []string{"--format-sort res:2160,vcodec:h264,acodec:m4a", "--merge-output-format mp4", "--sub-langs en", "--playlist-items 1-3"} {
		if !strings.Contains(s, want) {
			t.Errorf("video args lack %q: %s", want, s)
		}
	}
	if slices.Contains(a, "--no-playlist") {
		t.Error("--playlist still passes --no-playlist")
	}
	if _, err := Args("U", Options{Quality: "huge"}); err == nil {
		t.Error("bad quality accepted")
	}
}

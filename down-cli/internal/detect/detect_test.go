package detect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	dir := t.TempDir()
	tor := filepath.Join(dir, "x.torrent")
	other := filepath.Join(dir, "x.iso")
	os.WriteFile(tor, []byte("d"), 0o644)
	os.WriteFile(other, []byte("d"), 0o644)

	cases := []struct {
		raw     string
		kind    Kind
		audio   bool
		guessed bool
		wantErr bool
	}{
		{raw: "magnet:?xt=urn:btih:0123456789ABCDEF0123456789ABCDEF01234567&dn=Ubuntu", kind: Torrent},
		{raw: "magnet:?dn=nohash", wantErr: true},
		{raw: "https://www.youtube.com/watch?v=abc", kind: Media},
		{raw: "https://youtu.be/abc", kind: Media},
		{raw: "https://music.youtube.com/watch?v=abc", kind: Media, audio: true},
		{raw: "https://soundcloud.com/a/b", kind: Media, audio: true},
		{raw: "https://example.com/files/linux.iso", kind: HTTP, guessed: true},
		{raw: "https://example.com/a.torrent", kind: Torrent},
		{raw: "ftp://example.com/a.bin", kind: HTTP},
		{raw: "gopher://example.com/a", wantErr: true},
		{raw: tor, kind: Torrent},
		{raw: other, wantErr: true},
		{raw: "not a thing", wantErr: true},
	}
	for _, c := range cases {
		in, err := Parse(c.raw)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.raw, err)
			continue
		}
		if c.wantErr {
			continue
		}
		if in.Kind != c.kind || in.AudioFirst != c.audio || in.Guessed != c.guessed {
			t.Errorf("%s: got kind=%s audio=%v guessed=%v", c.raw, in.Kind, in.AudioFirst, in.Guessed)
		}
	}
}

func TestMagnetHash(t *testing.T) {
	hexIn, _ := Parse("magnet:?xt=urn:btih:0123456789ABCDEF0123456789ABCDEF01234567&tr=udp://a")
	if hexIn.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("hex hash = %s", hexIn.InfoHash)
	}
	// Same hash in base32, different trackers: same staging ID.
	b32, _ := Parse("magnet:?xt=urn:btih:AERUKZ4JVPG66AJDIVTYTK6N54ASGRLH&tr=udp://b")
	if b32.InfoHash != hexIn.InfoHash {
		t.Fatalf("base32 hash = %s", b32.InfoHash)
	}
	if hexIn.ID("") != b32.ID("") {
		t.Fatal("IDs differ for the same info hash")
	}
	if hexIn.ID("audio") == hexIn.ID("video") {
		t.Fatal("mode does not change the ID")
	}
}

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case "/nohead":
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/html")
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
		}
	}))
	defer srv.Close()
	for path, want := range map[string]Kind{"/page": Media, "/nohead": Media, "/file.bin": HTTP} {
		in, _ := Parse(srv.URL + path)
		if got := Probe(context.Background(), srv.Client(), in).Kind; got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}

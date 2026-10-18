package aria

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// slowReader makes the first download slow enough to interrupt.
type slowServer struct {
	data []byte
	slow bool
}

func (s *slowServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.slow {
		time.Sleep(50 * time.Millisecond)
	}
	http.ServeContent(w, r, "blob.bin", time.Unix(0, 0), bytes.NewReader(s.data))
}

func start(t *testing.T) *Client {
	t.Helper()
	c, err := Start(Options{CacheDir: t.TempDir(), Connections: 4, MaxDown: "2M"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// An interrupted HTTP download resumes from aria2's control file and ends
// byte-identical.
func TestHTTPResume(t *testing.T) {
	if _, err := exec.LookPath("aria2c"); err != nil {
		t.Skip("aria2c not installed")
	}
	data := make([]byte, 24<<20)
	rand.Read(data)
	srv := httptest.NewServer(&slowServer{data: data, slow: true})
	defer srv.Close()
	dir := t.TempDir()
	url := srv.URL + "/blob.bin"

	c := start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err := c.Download(ctx, Request{URI: url, Dir: dir})
	cancel()
	c.Close()
	if err == nil {
		t.Fatal("first download finished; make it slower")
	}
	if _, err := os.Stat(filepath.Join(dir, "blob.bin.aria2")); err != nil {
		t.Fatalf("no control file after interrupt: %v", err)
	}

	c = start(t)
	defer c.Close()
	var first int64 = -1
	res, err := c.Download(context.Background(), Request{URI: url, Dir: dir, Progress: func(p Progress) {
		if first < 0 && p.Done > 0 {
			first = p.Done
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "blob.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("resumed file differs")
	}
	if len(res.Files) != 1 || res.Name != "blob.bin" {
		t.Fatalf("result %+v", res)
	}
	if first <= 0 {
		t.Fatal("second run started from zero")
	}
	if _, err := os.Stat(filepath.Join(dir, "blob.bin.aria2")); !os.IsNotExist(err) {
		t.Fatal("control file left after completion")
	}
}

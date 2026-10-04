package archive

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "db"), 0o700)
	os.WriteFile(filepath.Join(src, "manifest.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(src, "db", "database.dump"), []byte("PGDMP..."), 0o600)

	var buf bytes.Buffer
	if err := Write(&buf, src); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := Extract(&buf, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "db", "database.dump"))
	if err != nil || string(b) != "PGDMP..." {
		t.Fatalf("round trip failed: %q %v", b, err)
	}
}

func TestRejectsPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "../evil", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	if err := Extract(&buf, t.TempDir()); err == nil {
		t.Fatal("path traversal accepted")
	}
}

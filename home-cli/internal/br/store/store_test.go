package store

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestWriteListFindRemove(t *testing.T) {
	dest := t.TempDir()
	t1 := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(24 * time.Hour)
	for _, ts := range []time.Time{t1, t2} {
		if _, err := Write(dest, "app", ts, "", func(w io.Writer) error { _, err := io.WriteString(w, ts.String()); return err }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Write(dest, "app", t2.Add(time.Hour), "prerestore", func(w io.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	all, _ := List(dest, "app")
	if len(all) != 3 || all[0].Tag != "prerestore" {
		t.Fatalf("unexpected listing: %+v", all)
	}
	latest, err := Find(dest, "app", "")
	if err != nil || !latest.Time.Equal(t2) {
		t.Fatalf("Find should return newest untagged backup, got %+v %v", latest, err)
	}
	if err := VerifyChecksum(latest); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(latest.Path, []byte("tampered"), 0o600)
	if err := VerifyChecksum(latest); err == nil {
		t.Fatal("tampering not detected")
	}
	if err := Remove(latest); err != nil {
		t.Fatal(err)
	}
	if all, _ := List(dest, "app"); len(all) != 2 {
		t.Fatalf("expected 2 after remove, got %d", len(all))
	}
}

func TestFailedWriteLeavesNothing(t *testing.T) {
	dest := t.TempDir()
	_, err := Write(dest, "app", time.Now(), "", func(w io.Writer) error { return errors.New("boom") })
	if err == nil {
		t.Fatal("expected error")
	}
	ents, _ := os.ReadDir(dest + "/app")
	if len(ents) != 0 {
		t.Fatalf("failed write left files behind: %v", ents)
	}
}

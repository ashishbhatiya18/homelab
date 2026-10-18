package clean

import (
	"archive/zip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/ashishbhatiya18/down/internal/config"
)

func write(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			r, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(r))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestIsJunk(t *testing.T) {
	junk := config.Default().Junk
	for rel, want := range map[string]bool{
		"Movie (2024)/Movie.mkv":               false,
		"Movie (2024)/Movie.nfo":               true,
		"Movie (2024)/Sample/movie.mkv":        true,
		"Movie (2024)/movie-sample.mkv":        true,
		"Movie (2024)/RARBG.txt":               true,
		"Movie (2024)/RARBG_DO_NOT_MIRROR.exe": true,
		"Album/cover.jpg":                      false,
		"Album/01 - Example.flac":              false,
		"Show/www.site.com.url":                true,
	} {
		if got := IsJunk(rel, junk); got != want {
			t.Errorf("IsJunk(%q) = %v", rel, got)
		}
	}
}

func TestVolumes(t *testing.T) {
	cases := map[string]bool{
		"a.zip": true, "a.7z": true, "a.rar": true,
		"a.part1.rar": true, "a.part01.rar": true, "a.part02.rar": false,
		"a.r00": false, "a.7z.001": true, "a.7z.002": false, "a.mkv": false,
	}
	for name, want := range cases {
		if got, _ := Volumes(name); got != want {
			t.Errorf("Volumes(%q) = %v", name, got)
		}
	}
	_, set := Volumes("Movie.part01.rar")
	for name, want := range map[string]bool{"Movie.part01.rar": true, "Movie.part12.rar": true, "Other.part02.rar": false} {
		if set.MatchString(name) != want {
			t.Errorf("set %q = %v", name, !want)
		}
	}
	_, set = Volumes("Movie.rar")
	if !set.MatchString("Movie.r07") || set.MatchString("Movie.mkv") {
		t.Error("old-style rar set")
	}
}

func TestTidyJunkAndEmptyDirs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "abc.torrent", "Show/S01E01.mkv", "Show/S01E01.nfo", "Show/Sample/s.mkv", "Show/Extras/RARBG.txt")
	acts, err := Tidy(root, Options{Junk: config.Default().Junk, Extract: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := tree(t, root); !slices.Equal(got, []string{"Show/S01E01.mkv"}) {
		t.Fatalf("tree = %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "Show", "Extras")); !os.IsNotExist(err) {
		t.Fatal("empty dir kept")
	}
	if len(acts) != 4 {
		t.Fatalf("actions = %+v", acts)
	}
}

func TestTidyDryRunAndPartial(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.mkv", "b.mkv.part", "c.iso.aria2", "readme.txt", ".down/x/y.part")
	acts, err := Tidy(root, Options{Junk: []string{"*.txt"}, Partial: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 4 || len(tree(t, root)) != 5 {
		t.Fatalf("dry run: acts=%+v tree=%v", acts, tree(t, root))
	}
	// Without Partial, leftovers and staging are not touched.
	if _, err := Tidy(root, Options{Junk: []string{"*.txt"}}); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, root); !slices.Equal(got, []string{".down/x/y.part", "a.mkv", "b.mkv.part", "c.iso.aria2"}) {
		t.Fatalf("tree = %v", got)
	}
}

func TestTidyExtract(t *testing.T) {
	root := t.TempDir()
	f, _ := os.Create(filepath.Join(root, "pack.zip"))
	zw := zip.NewWriter(f)
	for _, n := range []string{"Movie/Movie.mkv", "Movie/Movie.nfo"} {
		w, _ := zw.Create(n)
		w.Write([]byte(n))
	}
	zw.Close()
	f.Close()
	if _, err := Tidy(root, Options{Junk: config.Default().Junk, Extract: true}); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, root); !slices.Equal(got, []string{"Movie/Movie.mkv"}) {
		t.Fatalf("tree = %v", got)
	}
}

func TestPayloadFlatPublish(t *testing.T) {
	staging, out := t.TempDir(), t.TempDir()
	write(t, staging, "0123.torrent", "Movie (2024)/Movie.mkv")
	write(t, out, "Movie.mkv")
	rels, err := Payload(staging, false)
	if err != nil || !slices.Equal(rels, []string{"Movie (2024)"}) {
		t.Fatalf("payload = %v %v", rels, err)
	}
	flat := Flat(staging, rels[0])
	if flat != filepath.Join("Movie (2024)", "Movie.mkv") {
		t.Fatalf("flat = %q", flat)
	}
	got, err := Publish(staging, out, []string{flat}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "Movie (1).mkv"); !slices.Equal(got, []string{want}) {
		t.Fatalf("published %v", got)
	}
	if n, _ := Leftovers(staging); n != 1 {
		t.Fatalf("leftovers = %d", n)
	}
	if Flat(staging, "0123.torrent") != "" {
		t.Fatal("a file is not flattened")
	}
}

func TestUniqueDir(t *testing.T) {
	out := t.TempDir()
	os.Mkdir(filepath.Join(out, "Album.v2"), 0o755)
	if got := Unique(filepath.Join(out, "Album.v2")); got != filepath.Join(out, "Album.v2 (1)") {
		t.Fatalf("Unique = %s", got)
	}
}

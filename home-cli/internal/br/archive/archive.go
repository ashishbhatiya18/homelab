// Package archive packs a staging directory into a tar stream and back.
package archive

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Write streams every regular file under dir into w as a tar archive.
func Write(w io.Writer, dir string) error {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	tw := tar.NewWriter(w)
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		h := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

// Extract unpacks a tar stream into dir, rejecting unsafe paths.
func Extract(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.FromSlash(h.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(filepath.Clean(name), "..") {
			return fmt.Errorf("unsafe path in archive: %q", h.Name)
		}
		dst := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}

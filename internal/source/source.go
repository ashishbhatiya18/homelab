// Package source defines what can be backed up.
//
// Each kind of data (a Postgres database, a directory of files, …) is a Source
// implementation that registers a factory under a type name. Apps in the config
// are just lists of sources, so:
//
//   - adding an app is a config-only change, and
//   - adding a new kind of data is one new file in this package that calls
//     Register in its init(); the engine never changes.
//
// See docs/ADDING_SOURCES.md.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/ashishbhatiya18/hbr/internal/manifest"
	"github.com/ashishbhatiya18/hbr/internal/remote"
)

// Common holds the fields every source has in the config.
type Common struct {
	Type string `yaml:"type"`
	Name string `yaml:"name"`
	Host string `yaml:"host"` // key in the config's hosts; empty for local/direct sources
	// Restore controls whether `restore` writes this source back to production.
	// Sources like secrets default to false: they are extracted, never pushed.
	Restore *bool `yaml:"restore"`
}

// Env is what sources get to work with.
type Env struct {
	Remote *remote.Runner
	// ContainerCLI is the local docker/podman binary used for restore drills.
	ContainerCLI string
	// VerifyMajors lists Postgres major versions a drill restores into
	// (0 means "the version the backup came from").
	VerifyMajors []int
	// PgBin is the directory holding local Postgres client tools.
	PgBin string
	Logf  func(format string, args ...any)
}

// Target is an arbitrary destination for a portable restore, e.g. a brand-new
// Postgres server. Password is passed via the environment, never argv.
type Target struct {
	URL      string
	Password string
	Create   bool // create the database if it does not exist
	Replace  bool // allow restoring over a non-empty database
}

// PortableRestorer is implemented by sources that can restore into any
// compatible server, not just the one they were backed up from.
type PortableRestorer interface {
	// InspectTarget reports whether the target database exists and is empty.
	InspectTarget(ctx context.Context, env *Env, t Target) (exists, empty bool, err error)
	// DumpTarget writes the target's current contents (used as a safety copy).
	DumpTarget(ctx context.Context, env *Env, t Target, w io.Writer) error
	// RestoreTo restores the backup into the target and checks the result.
	RestoreTo(ctx context.Context, env *Env, dir string, a manifest.Artifact, t Target) (string, error)
}

type Source interface {
	Name() string
	Kind() string
	Host() string
	// Restorable reports whether Restore may write this source to production.
	Restorable() bool
	// Collect fetches the data into dir (empty, private) and describes it.
	// Implementations must validate what they fetched before returning.
	Collect(ctx context.Context, env *Env, dir string) (manifest.Artifact, error)
	// Restore pushes the data in dir back to its origin.
	Restore(ctx context.Context, env *Env, dir string, a manifest.Artifact) error
}

// Verifier is optionally implemented by sources that can prove a backup is
// usable without touching production (e.g. restore into a throwaway database).
type Verifier interface {
	Verify(ctx context.Context, env *Env, dir string, a manifest.Artifact) (string, error)
}

// Factory builds a Source from its YAML node.
type Factory func(common Common, node *yaml.Node) (Source, error)

var registry = map[string]Factory{}

// Register makes a source type available to the config. Call from init().
func Register(typ string, f Factory) {
	if _, dup := registry[typ]; dup {
		panic("source type registered twice: " + typ)
	}
	registry[typ] = f
}

// Types lists registered source types.
func Types() []string {
	t := make([]string, 0, len(registry))
	for k := range registry {
		t = append(t, k)
	}
	sort.Strings(t)
	return t
}

// Build turns an app's source nodes into Sources.
func Build(app string, nodes []yaml.Node, knownHosts map[string]string) ([]Source, error) {
	var out []Source
	names := map[string]bool{}
	for i := range nodes {
		var c Common
		if err := nodes[i].Decode(&c); err != nil {
			return nil, fmt.Errorf("app %s source #%d: %w", app, i+1, err)
		}
		f, ok := registry[c.Type]
		if !ok {
			return nil, fmt.Errorf("app %s source #%d: unknown type %q (known: %v)", app, i+1, c.Type, Types())
		}
		if c.Name == "" {
			return nil, fmt.Errorf("app %s source #%d: name is required", app, i+1)
		}
		if names[c.Name] {
			return nil, fmt.Errorf("app %s: duplicate source name %q", app, c.Name)
		}
		names[c.Name] = true
		if _, ok := knownHosts[c.Host]; c.Host != "" && !ok {
			return nil, fmt.Errorf("app %s source %s: unknown host %q", app, c.Name, c.Host)
		}
		s, err := f(c, &nodes[i])
		if err != nil {
			return nil, fmt.Errorf("app %s source %s: %w", app, c.Name, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// base implements the boring parts of Source for embedding.
type base struct {
	Common
	restoreDefault bool
}

func (b base) Name() string { return b.Common.Name }
func (b base) Kind() string { return b.Common.Type }
func (b base) Host() string { return b.Common.Host }
func (b base) Restorable() bool {
	if b.Common.Restore != nil {
		return *b.Common.Restore
	}
	return b.restoreDefault
}

// hashFiles returns archive-relative sha256 sums for every regular file under dir.
func hashFiles(dir, prefix string) (map[string]string, error) {
	sums := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		sum, err := fileSHA256(p)
		if err != nil {
			return err
		}
		sums[filepath.ToSlash(filepath.Join(prefix, rel))] = sum
		return nil
	})
	return sums, err
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkSums verifies files in dir against the artifact's recorded checksums.
func checkSums(dir string, a manifest.Artifact) error {
	for rel, want := range a.Files {
		got, err := fileSHA256(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if got != want {
			return fmt.Errorf("%s: checksum mismatch", rel)
		}
	}
	return nil
}

// CheckArtifact verifies an unpacked artifact's files against the checksums
// recorded at backup time.
func CheckArtifact(dir string, a manifest.Artifact) error { return checkSums(dir, a) }

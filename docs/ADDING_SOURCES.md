# Adding a new kind of source

home br's engine (backup, encryption, retention, drills, restore) only talks to
data through the `source.Source` interface in `internal/source/source.go`.
Adding support for, say, MySQL or a directory of files is one new file in
`internal/source/` — nothing else changes.

```go
package source

func init() {
	Register("mysql-docker", func(c Common, n *yaml.Node) (Source, error) {
		m := &mysqlDocker{base: base{Common: c, restoreDefault: true}}
		if err := n.Decode(m); err != nil {   // your own config fields
			return nil, err
		}
		return m, nil
	})
}

type mysqlDocker struct {
	base                       // provides Name, Kind, Host, Restorable
	Container string `yaml:"container"`
	Database  string `yaml:"database"`
}

// Collect writes the data into dir (private, empty), validates it, and
// returns an Artifact describing it (record checksums with hashFiles).
func (m *mysqlDocker) Collect(ctx context.Context, env *Env, dir string) (manifest.Artifact, error)

// Restore writes the data in dir back to its origin.
func (m *mysqlDocker) Restore(ctx context.Context, env *Env, dir string, a manifest.Artifact) error
```

Optional interfaces:

- `Verifier` — prove a snapshot is usable without touching production
  (used by `home br verify`).
- `PortableRestorer` — restore into an arbitrary target (used by
  `home br restore --to`).

Then use it in the config:

```yaml
apps:
  - name: myapp
    sources:
      - type: mysql-docker
        name: db
        host: myserver
        container: mysql
        database: myapp
```

Rules of thumb: never put credentials in the config (run commands inside the
container over SSH, or use the Keychain via `internal/secret`), validate what
you fetched before returning from `Collect`, and make `Restore` atomic where
the database allows it.

package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ashishbhatiya18/hbr/internal/remote"
)

// postgres-docker: a database inside a Docker container on a host reachable
// over passwordless SSH. pg_dump runs inside the container, so no database
// password is needed anywhere.
//
//   - type: postgres-docker
//     name: db
//     host: myserver          # key in `hosts`
//     container: postgres
//     user: postgres
//     database: mydb
type dockerExec struct {
	Container string `yaml:"container"`
	User      string `yaml:"user"`
	Database  string `yaml:"database"`
	host      string
}

func init() {
	Register("postgres-docker", func(c Common, n *yaml.Node) (Source, error) {
		x := &dockerExec{User: "postgres"}
		if err := n.Decode(x); err != nil {
			return nil, err
		}
		if c.Host == "" || x.Container == "" || x.Database == "" {
			return nil, errors.New("host, container and database are required")
		}
		x.host = c.Host
		return &pgSource{base: base{Common: c, restoreDefault: true}, x: x}, nil
	})
}

func (d *dockerExec) run(ctx context.Context, env *Env, tool string, args []string, stdin io.Reader, stdout io.Writer) error {
	full := append([]string{tool, "-U", d.User, "-d", d.Database}, args...)
	if tool == "pg_dump" {
		full = append(append([]string{tool, "-U", d.User}, args...), d.Database)
	}
	q := make([]string, len(full))
	for i, a := range full {
		q[i] = remote.Quote(a)
	}
	cmd := "docker exec -i " + remote.Quote(d.Container) + " " + strings.Join(q, " ")
	return env.Remote.Run(ctx, d.host, cmd, stdin, stdout)
}

func (d *dockerExec) database() string { return d.Database }

func (d *dockerExec) describe() string {
	return fmt.Sprintf("%s in container %s on %s", d.Database, d.Container, d.host)
}

// DockerQuery runs SQL against a container's Postgres; used by `hbr setup`
// to discover databases.
func DockerQuery(ctx context.Context, r *remote.Runner, host, container, user, db, sql string) (string, error) {
	x := &dockerExec{Container: container, User: user, Database: db, host: host}
	var out strings.Builder
	err := x.run(ctx, &Env{Remote: r}, "psql", []string{"-X", "-tA", "-c", sql}, nil, &out)
	return out.String(), err
}

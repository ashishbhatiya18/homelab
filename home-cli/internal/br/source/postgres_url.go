package source

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ashishbhatiya18/home/internal/br/secret"
)

// postgres-url: a database reachable directly from this machine. The
// password is read from the macOS Keychain (stored by `hbr setup`), never
// from the config file.
//
//   - type: postgres-url
//     name: db
//     url: postgres://backup_user@db.example.com:5432/mydb?sslmode=require
type urlExec struct {
	URL     string `yaml:"url"`
	account string
}

func init() {
	Register("postgres-url", func(c Common, n *yaml.Node) (Source, error) {
		x := &urlExec{}
		if err := n.Decode(x); err != nil {
			return nil, err
		}
		u, err := url.Parse(x.URL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path == "" || u.Path == "/" {
			return nil, errors.New("url must look like postgres://user@host:5432/dbname")
		}
		if _, hasPw := u.User.Password(); hasPw {
			return nil, errors.New("do not put the password in the url; `hbr setup` stores it in the Keychain")
		}
		return &pgSource{base: base{Common: c, restoreDefault: true}, x: x}, nil
	})
}

// KeychainAccount is the Keychain account name for a URL source's password.
func KeychainAccount(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.RawQuery = ""
	return u.String()
}

func (x *urlExec) run(ctx context.Context, env *Env, tool string, args []string, stdin io.Reader, stdout io.Writer) error {
	pw, err := secret.Get(KeychainAccount(x.URL))
	if err != nil {
		return err
	}
	return local(ctx, env, pw, stdin, stdout, tool, append(args, "-d", x.URL)...)
}

func (x *urlExec) database() string {
	u, err := url.Parse(x.URL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

func (x *urlExec) describe() string {
	u, _ := url.Parse(x.URL)
	return u.Redacted()
}

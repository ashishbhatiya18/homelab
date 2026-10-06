package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ashishbhatiya18/home/internal/br/manifest"
)

// pgExec runs Postgres client programs against one database. Implementations
// differ only in *how* the programs are reached (docker exec over SSH, or a
// direct connection), so all backup/restore logic below is shared.
type pgExec interface {
	// run executes a Postgres client (pg_dump, pg_restore, psql) with args
	// against the source database; the database is selected by the exec.
	run(ctx context.Context, env *Env, tool string, args []string, stdin io.Reader, stdout io.Writer) error
	describe() string
	database() string
}

// pgSource is the shared implementation behind every Postgres source type.
type pgSource struct {
	base
	x pgExec
}

const dumpFile = "database.dump"

// Portable dumps leave out ownership and privileges, so they restore cleanly
// into any Postgres of the same or newer major version without the original
// roles existing.
var portableDump = []string{"-Fc", "--no-owner", "--no-privileges", "--quote-all-identifiers"}
var portableRestore = []string{"--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction"}

func (p *pgSource) psql(ctx context.Context, env *Env, sql string) (string, error) {
	var out bytes.Buffer
	err := p.x.run(ctx, env, "psql", []string{"-X", "-tA", "-F", "\t", "-c", sql}, nil, &out)
	return out.String(), err
}

func (p *pgSource) Collect(ctx context.Context, env *Env, dir string) (manifest.Artifact, error) {
	a := manifest.Artifact{Source: p.Name(), Kind: p.Kind(), Host: p.Host(),
		Meta: map[string]any{"from": p.x.describe(), "database": p.x.database()}}
	ver, err := p.psql(ctx, env, "show server_version_num")
	if err != nil {
		return a, err
	}
	num, err := strconv.Atoi(strings.TrimSpace(ver))
	if err != nil {
		return a, fmt.Errorf("unexpected server version %q", ver)
	}
	a.Meta["server_major"] = num / 10000

	out := filepath.Join(dir, dumpFile)
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return a, err
	}
	err = p.x.run(ctx, env, "pg_dump", portableDump, nil, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return a, fmt.Errorf("pg_dump: %w", err)
	}
	if err := validateDump(env, out); err != nil {
		return a, err
	}
	counts, err := rowCounts(ctx, func(sql string) (string, error) { return p.psql(ctx, env, sql) })
	if err != nil {
		return a, fmt.Errorf("row counts: %w", err)
	}
	a.Meta["row_counts"] = counts
	a.Files, err = hashFiles(dir, "")
	return a, err
}

// Restore replaces the source database's contents in one transaction, so a
// failure leaves it untouched.
func (p *pgSource) Restore(ctx context.Context, env *Env, dir string, a manifest.Artifact) error {
	if err := checkSums(dir, a); err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(dir, dumpFile))
	if err != nil {
		return err
	}
	defer f.Close()
	return p.x.run(ctx, env, "pg_restore", append([]string{"--clean", "--if-exists"}, portableRestore...), f, nil)
}

// ---- counting ---------------------------------------------------------------

// countsSQL builds one query that counts rows in every user table.
const countsSQL = `select coalesce(string_agg(format('select %L, count(*) from %I.%I', table_schema||'.'||table_name, table_schema, table_name), ' union all '), 'select null, null') from information_schema.tables where table_type='BASE TABLE' and table_schema not in ('pg_catalog','information_schema')`

const userTablesSQL = `select count(*) from information_schema.tables where table_schema not in ('pg_catalog','information_schema')`

func rowCounts(ctx context.Context, query func(string) (string, error)) (map[string]int64, error) {
	q, err := query(countsSQL)
	if err != nil {
		return nil, err
	}
	out, err := query(strings.TrimSpace(q))
	if err != nil {
		return nil, err
	}
	return parseCounts(out), nil
}

func parseCounts(s string) map[string]int64 {
	m := map[string]int64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "\t")
		if !ok || k == "" {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			m[k] = n
		}
	}
	return m
}

// compareCounts fails if a table is missing or emptied. Counts are taken right
// after the dump, so small differences on busy tables are tolerated.
func compareCounts(want any, got map[string]int64) (string, error) {
	w := map[string]int64{}
	switch m := want.(type) {
	case map[string]any:
		for k, v := range m {
			if f, ok := v.(float64); ok {
				w[k] = int64(f)
			}
		}
	case map[string]int64:
		w = m
	}
	var problems []string
	for t, n := range w {
		g, ok := got[t]
		switch {
		case !ok:
			problems = append(problems, t+" missing")
		case n > 0 && g == 0:
			problems = append(problems, fmt.Sprintf("%s empty (expected ~%d rows)", t, n))
		}
	}
	var rows int64
	for _, g := range got {
		rows += g
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return "", fmt.Errorf("restored database differs: %s", strings.Join(problems, "; "))
	}
	return fmt.Sprintf("%d tables, %d rows", len(got), rows), nil
}

// validateDump checks the custom-format header and that pg_restore can read
// the archive's table of contents.
func validateDump(env *Env, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	hdr := make([]byte, 5)
	_, err = io.ReadFull(f, hdr)
	f.Close()
	if err != nil || string(hdr) != "PGDMP" {
		return errors.New("dump is empty or not a pg_dump custom-format archive")
	}
	var stderr bytes.Buffer
	cmd := exec.Command(pgTool(env, "pg_restore"), "--list", path)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore --list failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func pgTool(env *Env, name string) string {
	if env != nil && env.PgBin != "" {
		if p := filepath.Join(env.PgBin, name); fileExists(p) {
			return p
		}
	}
	return name
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// ---- portable restore into any server ---------------------------------------

// local runs a local Postgres client with the target's password in the
// environment (never on the command line).
func local(ctx context.Context, env *Env, password string, stdin io.Reader, stdout io.Writer, tool string, args ...string) error {
	var errb bytes.Buffer
	c := exec.CommandContext(ctx, pgTool(env, tool), args...)
	c.Env = append(os.Environ(), "PGPASSWORD="+password, "PGCONNECT_TIMEOUT=10")
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, &errb
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %v: %s", tool, err, strings.TrimSpace(errb.String()))
	}
	return nil
}

func localQuery(ctx context.Context, env *Env, password, dbURL, sql string) (string, error) {
	var out bytes.Buffer
	err := local(ctx, env, password, nil, &out, "psql", "-X", "-tA", "-F", "\t", "-d", dbURL, "-c", sql)
	return out.String(), err
}

// WithDatabase returns rawURL pointing at db instead.
func WithDatabase(rawURL, db string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("expected a postgres:// URL")
	}
	u.Path = "/" + db
	return u.String(), nil
}

func urlDatabase(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		return "", errors.New("the URL must name a database, e.g. postgres://user@host:5432/mydb")
	}
	return db, nil
}

func (p *pgSource) InspectTarget(ctx context.Context, env *Env, t Target) (bool, bool, error) {
	db, err := urlDatabase(t.URL)
	if err != nil {
		return false, false, err
	}
	admin, err := WithDatabase(t.URL, "postgres")
	if err != nil {
		return false, false, err
	}
	out, err := localQuery(ctx, env, t.Password, admin, fmt.Sprintf("select 1 from pg_database where datname = '%s'", strings.ReplaceAll(db, "'", "''")))
	if err != nil {
		return false, false, err
	}
	if strings.TrimSpace(out) != "1" {
		return false, true, nil
	}
	out, err = localQuery(ctx, env, t.Password, t.URL, userTablesSQL)
	if err != nil {
		return true, false, err
	}
	return true, strings.TrimSpace(out) == "0", nil
}

func (p *pgSource) DumpTarget(ctx context.Context, env *Env, t Target, w io.Writer) error {
	return local(ctx, env, t.Password, nil, w, "pg_dump", append(append([]string{}, portableDump...), "-d", t.URL)...)
}

func (p *pgSource) RestoreTo(ctx context.Context, env *Env, dir string, a manifest.Artifact, t Target) (string, error) {
	if err := checkSums(dir, a); err != nil {
		return "", err
	}
	exists, empty, err := p.InspectTarget(ctx, env, t)
	if err != nil {
		return "", err
	}
	db, _ := urlDatabase(t.URL)
	switch {
	case !exists && !t.Create:
		return "", fmt.Errorf("database %q does not exist on the target (add --create)", db)
	case !exists:
		admin, _ := WithDatabase(t.URL, "postgres")
		if _, err := localQuery(ctx, env, t.Password, admin, fmt.Sprintf(`create database "%s"`, strings.ReplaceAll(db, `"`, `""`))); err != nil {
			return "", err
		}
	case !empty && !t.Replace:
		return "", fmt.Errorf("database %q on the target is not empty (add --replace)", db)
	}
	if err := restoreLocalTools(ctx, env, filepath.Join(dir, dumpFile), t); err != nil {
		return "", err
	}
	got, err := rowCounts(ctx, func(sql string) (string, error) { return localQuery(ctx, env, t.Password, t.URL, sql) })
	if err != nil {
		return "", err
	}
	return compareCounts(a.Meta["row_counts"], got)
}

// restoreLocalTools restores a dump into t with this machine's Postgres
// tools. When those are newer than the target server, pg_restore can emit
// settings the server doesn't know (e.g. transaction_timeout, new in 17), so
// the dump is converted to SQL, unsupported settings are dropped, and the
// result is applied with psql in a single transaction.
func restoreLocalTools(ctx context.Context, env *Env, dump string, t Target) error {
	server, err := serverMajor(ctx, env, t)
	if err != nil {
		return err
	}
	client := clientMajor(env)
	clean := []string{}
	if t.Replace {
		clean = []string{"--clean", "--if-exists"}
	}
	if client <= server {
		f, err := os.Open(dump)
		if err != nil {
			return err
		}
		defer f.Close()
		args := append(append([]string{"-d", t.URL}, clean...), portableRestore...)
		return local(ctx, env, t.Password, f, nil, "pg_restore", args...)
	}

	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		var raw bytes.Buffer
		args := append(append([]string{"--no-owner", "--no-privileges", "-f", "-"}, clean...), dump)
		err := local(ctx, env, "", nil, &raw, "pg_restore", args...)
		if err == nil {
			err = filterSettings(&raw, pw, server)
		}
		pw.CloseWithError(err)
		errc <- err
	}()
	err = local(ctx, env, t.Password, pr, io.Discard, "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "--single-transaction", "-d", t.URL)
	if gerr := <-errc; err == nil {
		err = gerr
	}
	return err
}

// settingsSince lists session settings pg_restore may emit, by the server
// version that introduced them.
var settingsSince = map[string]int{"transaction_timeout": 17}

func filterSettings(in io.Reader, out io.Writer, server int) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<30)
	w := bufio.NewWriter(out)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "SET ") {
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "SET "), " ")
			if v, ok := settingsSince[name]; ok && server < v {
				continue
			}
		}
		w.WriteString(line)
		w.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return w.Flush()
}

func serverMajor(ctx context.Context, env *Env, t Target) (int, error) {
	admin, err := WithDatabase(t.URL, "postgres")
	if err != nil {
		return 0, err
	}
	out, err := localQuery(ctx, env, t.Password, admin, "show server_version_num")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("unexpected server version %q", out)
	}
	return n / 10000, nil
}

func clientMajor(env *Env) int {
	out, err := exec.Command(pgTool(env, "pg_restore"), "--version").Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(string(out)) // "pg_restore (PostgreSQL) 18.6"
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.Atoi(strings.SplitN(f[len(f)-1], ".", 2)[0])
	return v
}

// ---- restore drill ------------------------------------------------------------

// Verify restores the dump into throwaway local Postgres containers (the
// backup's own major version and any newer ones configured) and compares row
// counts with the manifest. Production is never touched.
func (p *pgSource) Verify(ctx context.Context, env *Env, dir string, a manifest.Artifact) (string, error) {
	if err := checkSums(dir, a); err != nil {
		return "", err
	}
	origin, _ := a.Meta["server_major"].(float64)
	set := map[int]bool{}
	for _, m := range env.VerifyMajors {
		if m == 0 {
			m = int(origin)
		}
		if m > 0 {
			set[m] = true
		}
	}
	if len(set) == 0 {
		set[int(origin)] = true
	}
	var majors []int
	for m := range set {
		majors = append(majors, m)
	}
	sort.Ints(majors)
	var results []string
	for _, m := range majors {
		res, err := drill(ctx, env, dir, a, m)
		if err != nil {
			return "", fmt.Errorf("postgres %d: %w", m, err)
		}
		results = append(results, fmt.Sprintf("pg%d: %s", m, res))
	}
	return strings.Join(results, "; "), nil
}

func drill(ctx context.Context, env *Env, dir string, a manifest.Artifact, major int) (string, error) {
	cli := env.ContainerCLI
	name := fmt.Sprintf("hbr-drill-%d-%d", major, time.Now().UnixNano())
	run := func(stdin io.Reader, args ...string) (string, error) {
		var out, errb bytes.Buffer
		c := exec.CommandContext(ctx, cli, args...)
		c.Stdin, c.Stdout, c.Stderr = stdin, &out, &errb
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("%s %s: %v: %s", cli, args[0], err, strings.TrimSpace(errb.String()))
		}
		return out.String(), nil
	}
	if _, err := run(nil, "run", "-d", "--rm", "--name", name, "-e", "POSTGRES_HOST_AUTH_METHOD=trust", fmt.Sprintf("postgres:%d", major)); err != nil {
		return "", err
	}
	defer exec.Command(cli, "rm", "-f", name).Run()

	// The image starts a temporary server for init, then the real one; wait
	// until queries succeed twice in a row.
	deadline := time.Now().Add(2 * time.Minute)
	for stable := 0; stable < 2; {
		if time.Now().After(deadline) {
			return "", errors.New("throwaway postgres did not become ready")
		}
		if _, err := run(nil, "exec", name, "psql", "-U", "postgres", "-tAc", "select 1"); err == nil {
			stable++
		} else {
			stable = 0
		}
		time.Sleep(2 * time.Second)
	}
	// A fresh database under an unrelated role proves the dump needs nothing
	// from the original server.
	if _, err := run(nil, "exec", name, "createdb", "-U", "postgres", "restored"); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(dir, dumpFile))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := run(f, append([]string{"exec", "-i", name, "pg_restore", "-U", "postgres", "-d", "restored"}, portableRestore...)...); err != nil {
		return "", err
	}
	got, err := rowCounts(ctx, func(sql string) (string, error) {
		return run(nil, "exec", name, "psql", "-U", "postgres", "-d", "restored", "-X", "-tA", "-F", "\t", "-c", sql)
	})
	if err != nil {
		return "", err
	}
	return compareCounts(a.Meta["row_counts"], got)
}

// Package secretfiles moves a stack's secret files on a node into a new layout,
// following the stack's secrets.migrate.yaml. Everything that touches a
// secret runs on the node: values are copied there and never cross SSH.
package secretfiles

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/ashishbhatiya18/home/internal/remote"
	"gopkg.in/yaml.v3"
)

// MigrateFile is the name of the mapping a stack ships next to compose.yaml.
const MigrateFile = "secrets.migrate.yaml"

// Move is one entry of secrets.migrate.yaml. Paths are relative to the
// node's secrets directory. With neither Key nor Keys, From (a file or a
// directory) is copied; with Key, that variable's value in the env file From
// becomes the secret file To; with Keys, those lines of From make the env file To.
type Move struct {
	From string   `yaml:"from"`
	To   string   `yaml:"to"`
	Key  string   `yaml:"key,omitempty"`
	Keys []string `yaml:"keys,omitempty"`
}

func (m Move) String() string {
	switch {
	case m.Key != "":
		return fmt.Sprintf("%s (%s) → %s", m.From, m.Key, m.To)
	case len(m.Keys) > 0:
		return fmt.Sprintf("%s (%s) → %s", m.From, strings.Join(m.Keys, ", "), m.To)
	}
	return m.From + " → " + m.To
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseMoves reads and checks a secrets.migrate.yaml.
func ParseMoves(b []byte) ([]Move, error) {
	var moves []Move
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&moves); err != nil {
		return nil, fmt.Errorf("%s: %w", MigrateFile, err)
	}
	if len(moves) == 0 {
		return nil, fmt.Errorf("%s: no moves", MigrateFile)
	}
	to := map[string]bool{}
	for i, m := range moves {
		where := fmt.Sprintf("%s: move %d", MigrateFile, i+1)
		for _, p := range []string{m.From, m.To} {
			if err := checkPath(p); err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
		}
		if m.From == m.To {
			return nil, fmt.Errorf("%s: from and to are the same", where)
		}
		if m.Key != "" && len(m.Keys) > 0 {
			return nil, fmt.Errorf("%s: key and keys are exclusive", where)
		}
		for _, k := range append([]string{m.Key}, m.Keys...) {
			if k != "" && !envKey.MatchString(k) {
				return nil, fmt.Errorf("%s: %q is not a variable name", where, k)
			}
		}
		if to[m.To] {
			return nil, fmt.Errorf("%s: %s is the target of two moves", where, m.To)
		}
		to[m.To] = true
	}
	return moves, nil
}

func checkPath(p string) error {
	switch {
	case p == "":
		return errors.New("from and to are required")
	case path.IsAbs(p):
		return fmt.Errorf("%s: paths are relative to the secrets directory", p)
	case path.Clean(p) != p || p == "." || strings.HasPrefix(p, "../") || p == "..":
		return fmt.Errorf("%s: not a clean path inside the secrets directory", p)
	}
	return nil
}

// Script returns the bash script that runs action ("status", "apply",
// "undo" or "cleanup") for stack's moves in the secrets directory dir on the
// node. live is the deployed stacks directory; cleanup deletes a move's
// source only when no compose.yaml there references it and no other stack's
// secrets.migrate.yaml still moves it. created lists what apply made, for undo.
//
// Output lines: "move <i> <state>" (status, apply), "created <path>",
// "removed <path>", "kept <path> <reason>". States: pending, copied (target
// matches the source), done (only the target is left), missing (no source),
// conflict:<reason>.
func Script(action, dir, live, stack string, moves []Move, created []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -euo pipefail\nS=%s LIVE=%s STACK=%s ACTION=%s\n", remote.Quote(dir), remote.Quote(live), remote.Quote(stack), action)
	b.WriteString(funcs)
	switch action {
	case "undo":
		for _, p := range created {
			fmt.Fprintf(&b, "undo %s\n", remote.Quote(p))
		}
	default:
		for i, m := range moves {
			kind, extra := "copy", ""
			switch {
			case m.Key != "":
				kind, extra = "key", remote.Quote(m.Key)
			case len(m.Keys) > 0:
				kind = "keys"
				for _, k := range m.Keys {
					extra += " " + remote.Quote(k)
				}
			}
			fmt.Fprintf(&b, "move %d %s %s %s %s\n", i+1, kind, remote.Quote(m.From), remote.Quote(m.To), extra)
		}
	}
	return b.String()
}

// funcs is the node side. Everything runs through sudo -n: secret files can
// belong to the users their containers run as.
const funcs = `
as() { sudo -n "$@"; }
# value KEY FILE: the value compose would read for KEY, without a newline.
# Refuses values it can't read the way compose does.
value() {
  as awk -v k="$1" '
    index($0, k "=") == 1 { found = 1; v = substr($0, length(k) + 2) }
    END {
      if (!found) { print "no " k " in the file" > "/dev/stderr"; exit 3 }
      if (v ~ /^'"'"'.*'"'"'$/) v = substr(v, 2, length(v) - 2)
      else if (v ~ /^".*"$/) {
        v = substr(v, 2, length(v) - 2)
        if (v ~ /[\\$]/) { print k ": double-quoted value with \\ or $, move it by hand" > "/dev/stderr"; exit 3 }
      } else {
        sub(/[ \t]+#.*$/, "", v); sub(/[ \t]+$/, "", v)
        # compose reads $$ as a literal $ and expands any other $.
        t = v; gsub(/\$\$/, "", t)
        if (t ~ /\$/) { print k ": unquoted value with $, move it by hand" > "/dev/stderr"; exit 3 }
        gsub(/\$\$/, "$", v)
      }
      printf "%s", v
    }' "$2"
}
# lines FILE KEY...: those KEY= lines of an env file, in file order.
lines() {
  local f=$1; shift
  as awk -v keys="$*" '
    BEGIN { n = split(keys, a, " "); for (i = 1; i <= n; i++) want[a[i]] = 1 }
    { k = $0; sub(/=.*/, "", k) }
    (k in want) && index($0, k "=") == 1 { print; got[k] = 1 }
    END { for (k in want) if (!(k in got)) { print "no " k " in the file" > "/dev/stderr"; exit 3 } }' "$f"
}
# render KIND FROM ARGS...: what the target should contain (key, keys).
render() {
  local kind=$1 from=$2; shift 2
  case $kind in key) value "$1" "$from" ;; keys) lines "$from" "$@" ;; esac
}
mkparent() {
  local d; d=$(dirname "$1")
  [ -d "$d" ] && return 0
  as install -d -m 700 -o "$(stat -c %u "$S")" -g "$(stat -c %g "$S")" "$d"
}
state() {
  local kind=$1 rel=$3 src=$2 from=$S/$2 to=$S/$3; shift 3
  local hf=no ht=no
  as test -e "$from" && hf=yes
  as test -e "$to" && ht=yes
  if [ $ht = no ]; then [ $hf = yes ] && echo pending || echo missing; return; fi
  if [ $hf = no ]; then echo done; return; fi
  case $kind in
    copy)
      if as test -d "$from"; then
        as diff -rq "$from" "$to" >/dev/null 2>&1 && echo copied || echo "conflict:$rel exists and differs"
      else
        as cmp -s "$from" "$to" && echo copied || echo "conflict:$rel exists and differs"
      fi ;;
    *)
      local want; want=$(render "$kind" "$from" "$@" | sha256sum) || { echo "conflict:can't read $src"; return; }
      [ "$want" = "$(as cat "$to" | sha256sum)" ] && echo copied || echo "conflict:$rel exists and differs" ;;
  esac
}
apply() {
  local kind=$1 rel=$3 from=$S/$2 to=$S/$3; shift 3
  mkparent "$to"
  case $kind in
    copy) as cp -a "$from" "$to" ;;
    *)
      local tmp; tmp=$(as mktemp "$(dirname "$to")/.migrate.XXXXXX")
      if ! render "$kind" "$from" "$@" | as tee "$tmp" >/dev/null; then as rm -f "$tmp"; return 3; fi
      as chown --reference="$from" "$tmp"
      as chmod 600 "$tmp"
      as mv "$tmp" "$to" ;;
  esac
  echo "created $rel"
}
referenced() {
  # Is $S/$1 (or anything under it) still used by a deployed stack?
  local p=$S/$1 f
  for f in "$LIVE"/*/compose.yaml; do
    [ -f "$f" ] || continue
    grep -v '^[[:space:]]*#' "$f" | sed 's/[[:space:]]#.*//' | grep -qF -- "$p" && { echo "${f#$LIVE/}"; return 0; }
  done
  for f in "$LIVE"/*/secrets.migrate.yaml; do
    [ -f "$f" ] && [ "$f" != "$LIVE/$STACK/secrets.migrate.yaml" ] || continue
    grep -qE "^[[:space:]]*-?[[:space:]]*from:[[:space:]]*['\"]?$1['\"]?[[:space:]]*$" "$f" && { echo "${f#$LIVE/}"; return 0; }
  done
  return 1
}
move() {
  local i=$1 kind=$2 from=$3 to=$4; shift 4
  local st; st=$(state "$kind" "$from" "$to" "$@")
  case $ACTION in
    status) echo "move $i $st" ;;
    apply)
      if [ "$st" = pending ]; then apply "$kind" "$from" "$to" "$@"; st=$(state "$kind" "$from" "$to" "$@"); fi
      echo "move $i $st" ;;
    cleanup)
      [ "$st" = copied ] || return 0
      local by
      if by=$(referenced "$from"); then echo "kept $from used by $by"; else as rm -rf -- "$S/$from"; echo "removed $from"; fi ;;
  esac
}
undo() { as rm -rf -- "$S/$1"; echo "removed $1"; }
`

// Result is the parsed output of a Script run.
type Result struct {
	States  map[int]string // move number (1-based) → state
	Created []string
	Removed []string
	Kept    []string // "path used by stack/compose.yaml"
}

// ParseResult reads a Script's output.
func ParseResult(out string) Result {
	r := Result{States: map[int]string{}}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "move":
			var i int
			if _, err := fmt.Sscan(f[1], &i); err == nil && len(f) > 2 {
				r.States[i] = strings.Join(f[2:], " ")
			}
		case "created":
			r.Created = append(r.Created, f[1])
		case "removed":
			r.Removed = append(r.Removed, f[1])
		case "kept":
			r.Kept = append(r.Kept, strings.Join(f[1:], " "))
		}
	}
	return r
}

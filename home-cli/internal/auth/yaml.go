// Package auth reads and edits Authelia's node-side files — users.yml (the
// file authentication backend) and clients.yml (OpenID Connect clients,
// loaded as a second configuration file) — and parses the filesystem
// notifier's notification.txt. Edits work on yaml.Node trees, so comments
// and the layout of everything else survive.
package auth

import (
	"bytes"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// doc is a parsed YAML file whose top level is a mapping.
type doc struct{ top, root *yaml.Node }

func parse(b []byte, name string) (*doc, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if len(n.Content) == 0 { // empty file
		root := &yaml.Node{Kind: yaml.MappingNode}
		return &doc{top: &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, root: root}, nil
	}
	if n.Kind != yaml.DocumentNode || n.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: not a YAML mapping", name)
	}
	return &doc{top: &n, root: n.Content[0]}, nil
}

func (d *doc) bytes() []byte {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	enc.Encode(d.top)
	enc.Close()
	return buf.Bytes()
}

// get returns the value under key in mapping m, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// set puts v under key in mapping m, replacing an existing value in place.
func set(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v.HeadComment, v.LineComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, str(key), v)
}

// del removes key from mapping m and reports whether it was there.
func del(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// child returns the mapping under key in m, creating it when missing.
func child(m *yaml.Node, key string) *yaml.Node {
	if c := get(m, key); c != nil && c.Kind == yaml.MappingNode {
		return c
	}
	c := &yaml.Node{Kind: yaml.MappingNode}
	set(m, key, c)
	return c
}

func str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

// quoted is a single-quoted string, for values YAML would otherwise
// reinterpret (hashes with $, template calls).
func quoted(s string) *yaml.Node {
	n := str(s)
	n.Style = yaml.SingleQuotedStyle
	return n
}

func boolean(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(b)}
}

func list(items []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, s := range items {
		n.Content = append(n.Content, str(s))
	}
	return n
}

// flowList is a list written inline ([a, b]).
func flowList(items []string) *yaml.Node {
	n := list(items)
	n.Style = yaml.FlowStyle
	return n
}

func values(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		return []string{n.Value}
	}
	var out []string
	for _, c := range n.Content {
		out = append(out, c.Value)
	}
	return out
}

func scalar(m *yaml.Node, key string) string {
	if v := get(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidName reports whether s is usable as a username or client ID: it ends
// up in file names and shell commands on the node.
func ValidName(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("%q: use lowercase letters, digits, '.', '_' and '-' (starting with a letter or digit)", s)
	}
	return nil
}

package cloud

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Edits change a file in place at the YAML-node level, so comments and the
// layout of everything else survive.

type doc struct {
	path string
	root *yaml.Node // the document's top mapping
	top  *yaml.Node // the document node
}

func openDoc(path string) (*doc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if n.Kind != yaml.DocumentNode || len(n.Content) == 0 || n.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: not a YAML mapping", path)
	}
	return &doc{path: path, top: &n, root: n.Content[0]}, nil
}

// seq returns the sequence under key, creating it when missing.
func (d *doc) seq(key string) *yaml.Node {
	for i := 0; i+1 < len(d.root.Content); i += 2 {
		if d.root.Content[i].Value == key {
			v := d.root.Content[i+1]
			if v.Kind != yaml.SequenceNode {
				*v = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			}
			return v
		}
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	d.root.Content = append(d.root.Content, k, v)
	return v
}

func (d *doc) save() error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d.top); err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}

func nodeOf(v any) (*yaml.Node, error) {
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return n, nil
}

func ruleHost(n *yaml.Node) (host, path string) {
	if n.Kind == yaml.ScalarNode {
		return n.Value, ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "hostname":
			host = n.Content[i+1].Value
		case "path":
			path = n.Content[i+1].Value
		}
	}
	return host, path
}

// ErrNoRule: the tunnel file has no rule for the hostname.
var ErrNoRule = errors.New("no such route")

// SetRule adds r to a tunnel file before its catch-all rule, or replaces the
// rule for the same hostname and path. It returns the previous rule, if any.
func SetRule(dir, tunnel string, r Rule) (*Rule, error) {
	d, err := openDoc(TunnelPath(dir, tunnel))
	if err != nil {
		return nil, err
	}
	in := d.seq("ingress")
	n, err := nodeOf(r)
	if err != nil {
		return nil, err
	}
	for i, item := range in.Content {
		if h, p := ruleHost(item); h == r.Hostname && p == r.Path {
			var old Rule
			if err := item.Decode(&old); err != nil {
				return nil, err
			}
			n.HeadComment, n.LineComment = item.HeadComment, item.LineComment
			in.Content[i] = n
			return &old, d.save()
		}
	}
	at := len(in.Content)
	if at > 0 {
		if h, _ := ruleHost(in.Content[at-1]); h == "" {
			at-- // before the catch-all
		}
	} else {
		catchAll, _ := nodeOf(Rule{Service: "http_status:404"})
		in.Content = append(in.Content, catchAll)
	}
	in.Content = append(in.Content[:at], append([]*yaml.Node{n}, in.Content[at:]...)...)
	return nil, d.save()
}

// RemoveRule deletes the rule for host (any path when path is "*") and
// returns it.
func RemoveRule(dir, tunnel, host, path string) (*Rule, error) {
	d, err := openDoc(TunnelPath(dir, tunnel))
	if err != nil {
		return nil, err
	}
	in := d.seq("ingress")
	for i, item := range in.Content {
		if h, p := ruleHost(item); h == host && (p == path || path == "*") {
			var old Rule
			if err := item.Decode(&old); err != nil {
				return nil, err
			}
			in.Content = append(in.Content[:i], in.Content[i+1:]...)
			return &old, d.save()
		}
	}
	return nil, ErrNoRule
}

// AddRecord appends a record to a zone file, creating the file if needed.
func AddRecord(dir, zone string, r Record) error {
	p := ZonePath(dir, zone)
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		_, err := SaveZone(dir, ZoneFile{Zone: zone, Records: []Record{r}})
		return err
	}
	d, err := openDoc(p)
	if err != nil {
		return err
	}
	n, err := nodeOf(r)
	if err != nil {
		return err
	}
	recs := d.seq("records")
	recs.Content = append(recs.Content, n)
	return d.save()
}

// RemoveRecords deletes the records match selects and returns them.
func RemoveRecords(dir, zone string, match func(Record) bool) ([]Record, error) {
	d, err := openDoc(ZonePath(dir, zone))
	if err != nil {
		return nil, err
	}
	recs := d.seq("records")
	var kept []*yaml.Node
	var removed []Record
	for _, item := range recs.Content {
		var r Record
		if err := item.Decode(&r); err != nil {
			return nil, err
		}
		if match(r) {
			removed = append(removed, r)
			continue
		}
		kept = append(kept, item)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	recs.Content = kept
	return removed, d.save()
}

// RuleFor finds which tunnel file routes host (with any path).
func (fs *Files) RuleFor(host string) (*TunnelFile, *Rule) {
	host = strings.ToLower(host)
	for i := range fs.Tunnels {
		for j := range fs.Tunnels[i].Ingress {
			if strings.ToLower(fs.Tunnels[i].Ingress[j].Hostname) == host {
				return &fs.Tunnels[i], &fs.Tunnels[i].Ingress[j]
			}
		}
	}
	return nil, nil
}

// Resolved returns r as a full rule, with the file's defaults applied to a
// bare hostname.
func (f *TunnelFile) Resolved(r Rule) Rule {
	if !r.Short || f.Defaults == nil {
		r.Short = false
		return r
	}
	return Rule{Hostname: r.Hostname, Service: f.Defaults.Service, OriginRequest: fill(f.Defaults.OriginRequest, r.Hostname)}
}

// Shorten returns r as a bare hostname when the file's defaults produce
// exactly it.
func (f *TunnelFile) Shorten(r Rule) Rule {
	if f.Defaults != nil && r.Path == "" && r.Hostname != "" &&
		canon(Defaults{Service: r.Service, OriginRequest: clean(template(r.OriginRequest, r.Hostname))}) ==
			canon(Defaults{Service: f.Defaults.Service, OriginRequest: clean(f.Defaults.OriginRequest)}) {
		return Rule{Hostname: r.Hostname, Short: true}
	}
	r.Short = false
	return r
}

// SetField sets a top-level scalar of a tunnel file, adding it after
// `container:` (or at the end) when missing.
func SetField(dir, tunnel, key, value string) error {
	d, err := openDoc(TunnelPath(dir, tunnel))
	if err != nil {
		return err
	}
	c := d.root.Content
	for i := 0; i+1 < len(c); i += 2 {
		if c[i].Value == key {
			c[i+1].Kind, c[i+1].Tag, c[i+1].Value, c[i+1].Style = yaml.ScalarNode, "!!str", value, 0
			return d.save()
		}
	}
	at := len(c)
	for i := 0; i+1 < len(c); i += 2 {
		if c[i].Value == "container" {
			at = i + 2
		}
	}
	kv := []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, {Kind: yaml.ScalarNode, Tag: "!!str", Value: value}}
	d.root.Content = append(c[:at], append(kv, c[at:]...)...)
	return d.save()
}

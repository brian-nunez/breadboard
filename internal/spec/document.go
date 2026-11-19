package spec

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/errs"
)

// Document is experiment.yaml as a YAML node tree. Edits change the tree in
// place, so comments and key order survive a round trip.
type Document struct {
	root *yaml.Node // document node
}

// New returns a document for a fresh experiment.
func New(name string) *Document {
	var doc yaml.Node

	src := fmt.Sprintf("version: %d\nname: placeholder\n\n# Infra services from the catalog. Add one with `bb add <type>`.\nservices: {}\n\n# Your code. Each app is a compose service built from a directory with a Dockerfile.\napps: {}\n", CurrentVersion)
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		panic(err) // constant input
	}

	d := &Document{root: &doc}
	// Set through a node so names YAML would read as another type
	// (null, true, 123) are quoted.
	*lookup(d.top(), "name") = *scalar(name)

	return d
}

// Parse reads a document. A YAML syntax error is a validation error.
func Parse(data []byte) (*Document, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Invalid("", "invalid YAML: %v", err)
	}

	if doc.Kind == 0 {
		return nil, Invalid("", "the file is empty")
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, Invalid("", "the top level must be a mapping")
	}

	return &Document{root: &doc}, nil
}

// Bytes encodes the document with two-space indentation.
func (d *Document) Bytes() ([]byte, error) {
	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(d.root); err != nil {
		return nil, fmt.Errorf("encode experiment.yaml: %w", err)
	}

	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode experiment.yaml: %w", err)
	}

	return buf.Bytes(), nil
}

func (d *Document) top() *yaml.Node { return d.root.Content[0] }

// Version returns the declared spec version, or 0 when it is missing or not an integer.
func (d *Document) Version() int {
	n := lookup(d.top(), "version")
	if n == nil {
		return 0
	}

	v, err := strconv.Atoi(n.Value)
	if err != nil {
		return 0
	}

	return v
}

// HasName reports whether a service or app already uses name.
func (d *Document) HasName(name string) bool {
	for _, section := range []string{"services", "apps"} {
		if m := lookup(d.top(), section); m != nil && m.Kind == yaml.MappingNode && lookup(m, name) != nil {
			return true
		}
	}

	return false
}

// AddService appends an infra service, with an upstream entry for each
// named service (proxies only). The name must be unused.
func (d *Document) AddService(name, typ string, upstreams ...string) error {
	if d.HasName(name) {
		return fmt.Errorf("service %q: %w", name, errs.ErrExists)
	}

	m := d.section("services")
	body := mapping(scalar("type"), scalar(typ))

	if len(upstreams) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, u := range upstreams {
			seq.Content = append(seq.Content, mapping(scalar("service"), scalar(u)))
		}

		body.Content = append(body.Content, scalar("upstreams"), seq)
	}

	m.Content = append(m.Content, scalar(name), body)

	return nil
}

// AddProxy appends a proxy service with its config (keys sorted),
// upstreams, file and static_file. An upstream with only a service is
// written in the short form; the others as mappings with the fields that
// are set. The name must be unused.
func (d *Document) AddProxy(svc Service) error {
	if d.HasName(svc.Name) {
		return fmt.Errorf("service %q: %w", svc.Name, errs.ErrExists)
	}

	body := mapping(scalar("type"), scalar(svc.Type))
	if len(svc.Config) > 0 {
		cfg := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range slices.Sorted(maps.Keys(svc.Config)) {
			cfg.Content = append(cfg.Content, scalar(k), scalar(svc.Config[k]))
		}

		body.Content = append(body.Content, scalar("config"), cfg)
	}

	if len(svc.Upstreams) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}

		for _, u := range svc.Upstreams {
			if u.Port != 0 || u.Name != "" || u.TLS != (TLS{}) {
				seq.Style = 0
			}
		}

		for _, u := range svc.Upstreams {
			seq.Content = append(seq.Content, upstreamNode(u))
		}

		body.Content = append(body.Content, scalar("upstreams"), seq)
	}

	if svc.File != "" {
		body.Content = append(body.Content, scalar("file"), scalar(svc.File))
	}

	if svc.StaticFile != "" {
		body.Content = append(body.Content, scalar("static_file"), scalar(svc.StaticFile))
	}

	if svc.Host != nil {
		body.Content = append(body.Content, scalar("host"), hostNode(*svc.Host))
	}

	m := d.section("services")
	m.Content = append(m.Content, scalar(svc.Name), body)

	return nil
}

// upstreamNode writes one upstreams entry with the keys the decoder reads.
func upstreamNode(u Upstream) *yaml.Node {
	if u.Port == 0 && u.Name == "" && u.TLS == (TLS{}) {
		return scalar(u.Service)
	}

	n := mapping(scalar("service"), scalar(u.Service))
	if u.Port != 0 {
		n.Content = append(n.Content, scalar("port"), intScalar(u.Port))
	}

	if u.Name != "" {
		n.Content = append(n.Content, scalar("name"), scalar(u.Name))
	}

	if u.TLS != (TLS{}) {
		tls := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if u.TLS.Mode != "" {
			tls.Content = append(tls.Content, scalar("mode"), scalar(u.TLS.Mode))
		}

		if u.TLS.Reason != "" {
			tls.Content = append(tls.Content, scalar("reason"), scalar(u.TLS.Reason))
		} else {
			tls.Style = yaml.FlowStyle
		}

		n.Content = append(n.Content, scalar("tls"), tls)
	}

	return n
}

// AddApp appends an app. The name must be unused.
func (d *Document) AddApp(app App) error {
	if d.HasName(app.Name) {
		return fmt.Errorf("app %q: %w", app.Name, errs.ErrExists)
	}

	body := mapping(scalar("path"), scalar(app.Path))
	if len(app.Command) > 0 {
		body.Content = append(body.Content, scalar("command"), flowSeq(app.Command...))
	}

	if len(app.Uses) > 0 {
		uses := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		short := true

		for _, u := range app.Uses {
			if u.TLS != (TLS{}) {
				short = false
			}
		}

		if short {
			uses.Style = yaml.FlowStyle
		}

		for _, u := range app.Uses {
			if u.TLS == (TLS{}) {
				uses.Content = append(uses.Content, scalar(u.Service))

				continue
			}

			tls := mapping(scalar("mode"), scalar(u.TLS.Mode))
			if u.TLS.Reason != "" {
				tls.Content = append(tls.Content, scalar("reason"), scalar(u.TLS.Reason))
			}

			uses.Content = append(uses.Content, mapping(scalar("service"), scalar(u.Service), scalar("tls"), tls))
		}

		body.Content = append(body.Content, scalar("uses"), uses)
	}

	if len(app.Ports) > 0 {
		ports := make([]string, len(app.Ports))
		for i, p := range app.Ports {
			ports[i] = strconv.Itoa(p)
		}

		seq := flowSeq(ports...)
		for _, n := range seq.Content {
			n.Tag = "!!int"
		}

		body.Content = append(body.Content, scalar("ports"), seq)
	}

	if app.Health != nil {
		h := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if app.Health.Path != "" {
			h.Content = append(h.Content, scalar("path"), scalar(app.Health.Path))
		}

		if app.Health.Port != 0 {
			h.Content = append(h.Content, scalar("port"), intScalar(app.Health.Port))
		}

		body.Content = append(body.Content, scalar("health"), h)
	}

	if app.Register != nil {
		r := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if app.Register.Name != "" {
			r.Content = append(r.Content, scalar("name"), scalar(app.Register.Name))
		}

		r.Content = append(r.Content, scalar("port"), intScalar(app.Register.Port))
		if app.Register.Check != "" {
			r.Content = append(r.Content, scalar("check"), scalar(app.Register.Check))
		}

		if len(app.Register.Tags) > 0 {
			r.Content = append(r.Content, scalar("tags"), flowSeq(app.Register.Tags...))
		}

		body.Content = append(body.Content, scalar("register"), r)
	}

	if r := resourcesNode(app.Resources); r != nil {
		body.Content = append(body.Content, scalar("resources"), r)
	}

	if app.Replicas > 1 {
		body.Content = append(body.Content, scalar("replicas"), intScalar(app.Replicas))
	}

	if len(app.Sidecars) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, sc := range app.Sidecars {
			seq.Content = append(seq.Content, sidecarNode(sc))
		}

		body.Content = append(body.Content, scalar("sidecars"), seq)
	}

	if app.Host != nil {
		body.Content = append(body.Content, scalar("host"), hostNode(*app.Host))
	}

	m := d.section("apps")
	m.Content = append(m.Content, scalar(app.Name), body)

	return nil
}

// Remove deletes the service or app called name and reports which section held it.
func (d *Document) Remove(name string) (section string, err error) {
	for _, s := range []string{"services", "apps"} {
		m := lookup(d.top(), s)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}

		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == name {
				m.Content = append(m.Content[:i], m.Content[i+2:]...)
				if len(m.Content) == 0 {
					m.Style = yaml.FlowStyle
				}

				return s, nil
			}
		}
	}

	return "", fmt.Errorf("%q is not a service or app in experiment.yaml: %w", name, errs.ErrNotFound)
}

// SetVersion rewrites the version field.
func (d *Document) SetVersion(v int) {
	if n := lookup(d.top(), "version"); n != nil {
		n.Value = strconv.Itoa(v)
		n.Tag = "!!int"
		n.Style = 0

		return
	}

	top := d.top()
	top.Content = append([]*yaml.Node{scalar("version"), intScalar(v)}, top.Content...)
}

// section returns the services or apps mapping, creating it when missing.
func (d *Document) section(name string) *yaml.Node {
	top := d.top()
	if m := lookup(top, name); m != nil {
		if m.Kind == yaml.MappingNode {
			m.Style = 0 // an empty {} becomes block style once it has entries

			return m
		}
		// A null or scalar placeholder (e.g. "services:") becomes a mapping.
		*m = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: m.HeadComment, LineComment: m.LineComment, FootComment: m.FootComment}

		return m
	}

	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	top.Content = append(top.Content, scalar(name), m)

	return m
}

func lookup(m *yaml.Node, key string) *yaml.Node {
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

func scalar(v string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	// Quote values YAML would otherwise read as another type or a different string.
	var probe any
	if err := yaml.Unmarshal([]byte(v), &probe); err != nil {
		n.Style = yaml.DoubleQuotedStyle
	} else if s, ok := probe.(string); !ok || s != v {
		n.Style = yaml.DoubleQuotedStyle
	}

	return n
}

func intScalar(v int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
}

// hostNode writes host: true, or {port: <n>} when a port is set.
func hostNode(h Host) *yaml.Node {
	if h.Port == 0 {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}
	}

	n := mapping(scalar("port"), intScalar(h.Port))
	n.Style = yaml.FlowStyle

	return n
}

func mapping(kv ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: kv}
}

func flowSeq(items ...string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, it := range items {
		n.Content = append(n.Content, scalar(it))
	}

	return n
}

// sidecarNode writes one sidecars entry with the keys the decoder reads,
// leaving out fields that are unset so their defaults apply.
func sidecarNode(sc Sidecar) *yaml.Node {
	n := mapping(scalar("type"), scalar(sc.Type))
	if sc.Name != "" {
		n.Content = append(n.Content, scalar("name"), scalar(sc.Name))
	}

	add := func(k string, v *yaml.Node) { n.Content = append(n.Content, scalar(k), v) }

	switch {
	case sc.Envoy != nil:
		e := sc.Envoy
		if e.Inbound != nil {
			in := mapping(scalar("port"), intScalar(e.Inbound.Port), scalar("app_port"), intScalar(e.Inbound.AppPort))
			in.Style = yaml.FlowStyle
			add("inbound", in)
		}

		if len(e.Outbound) > 0 {
			seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}

			for _, o := range e.Outbound {
				on := mapping(scalar("service"), scalar(o.Service), scalar("port"), intScalar(o.Port))
				if o.LB != "" {
					on.Content = append(on.Content, scalar("lb"), scalar(o.LB))
				}

				if o.Timeout != "" {
					on.Content = append(on.Content, scalar("timeout"), scalar(o.Timeout))
				}

				if o.Retries != nil {
					on.Content = append(on.Content, scalar("retries"), intScalar(*o.Retries))
				}

				if o.Outlier != nil {
					ol := mapping(scalar("consecutive_5xx"), intScalar(o.Outlier.Consecutive5xx), scalar("ejection"), scalar(o.Outlier.Ejection))
					if o.Outlier.MaxEjectionPercent != 0 {
						ol.Content = append(ol.Content, scalar("max_ejection_percent"), intScalar(o.Outlier.MaxEjectionPercent))
					}

					if o.Outlier.Interval != "" {
						ol.Content = append(ol.Content, scalar("interval"), scalar(o.Outlier.Interval))
					}

					ol.Style = yaml.FlowStyle
					on.Content = append(on.Content, scalar("outlier"), ol)
				}

				seq.Content = append(seq.Content, on)
			}

			add("outbound", seq)
		}

		if t := e.Transparent; t != nil {
			if t.Egress == "" {
				add("transparent", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
			} else {
				tn := mapping(scalar("egress"), scalar(t.Egress))
				tn.Style = yaml.FlowStyle
				add("transparent", tn)
			}
		}

		if e.File != "" {
			add("file", scalar(e.File))
		}
	case sc.PgBouncer != nil:
		p := sc.PgBouncer
		add("service", scalar(p.Service))

		if p.PoolMode != "" {
			add("pool_mode", scalar(p.PoolMode))
		}

		if p.Port != 0 {
			add("port", intScalar(p.Port))
		}

		if p.PoolSize != 0 {
			add("pool_size", intScalar(p.PoolSize))
		}
	case sc.FluentBit != nil:
		add("to", scalar(sc.FluentBit.To))
		add("bucket", scalar(sc.FluentBit.Bucket))
	}

	return n
}

func resourcesNode(r Resources) *yaml.Node {
	if r == (Resources{}) {
		return nil
	}

	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if r.Memory != "" {
		n.Content = append(n.Content, scalar("memory"), scalar(r.Memory))
	}

	if r.CPUs != "" {
		n.Content = append(n.Content, scalar("cpus"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: r.CPUs})
	}

	return n
}

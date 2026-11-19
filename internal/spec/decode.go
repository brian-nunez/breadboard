package spec

import (
	"fmt"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/errs"
)

// Keys accepted by the schema's shape but not supported yet, with the phase
// that introduces each one.
var reservedTLS = map[string]string{"clientAuth": "Phase 6 (mTLS)", "ca": "Phase 6 (multiple CAs)"}

// Decode turns the document into a Spec. It reports every structural problem
// (unknown keys, wrong types, missing required fields) with its YAML path.
// A version newer than CurrentVersion returns errs.ErrNewerFormat. An older
// version is upgraded in memory; the file itself changes only through Migrate.
func (d *Document) Decode() (*Spec, error) {
	doc := d

	v := d.Version()
	switch {
	case v > CurrentVersion:
		return nil, fmt.Errorf("experiment.yaml version %d: %w", v, errs.ErrNewerFormat)
	case v >= 1 && v < CurrentVersion:
		clone, err := d.clone()
		if err != nil {
			return nil, err
		}

		if _, err := clone.upgrade(); err != nil {
			return nil, err
		}

		doc = clone
	}

	dec := &decoder{}

	s := dec.spec(doc.top())
	if err := violations(dec.vs); err != nil {
		return nil, err
	}

	return s, nil
}

func (d *Document) clone() (*Document, error) {
	b, err := d.Bytes()
	if err != nil {
		return nil, err
	}

	return Parse(b)
}

type decoder struct {
	vs []Violation
}

func (dec *decoder) add(path, format string, args ...any) {
	dec.vs = append(dec.vs, Violation{Path: path, Message: fmt.Sprintf(format, args...)})
}

// fields walks a mapping, reporting unknown and reserved keys, and returns the
// known keys' value nodes.
func (dec *decoder) fields(path string, n *yaml.Node, reserved map[string]string, known ...string) map[string]*yaml.Node {
	return dec.fieldsMsg(path, n, reserved, func(string) string { return "unknown key" }, known...)
}

// fieldsMsg is fields with a custom message for unknown keys.
func (dec *decoder) fieldsMsg(path string, n *yaml.Node, reserved map[string]string, unknown func(key string) string, known ...string) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}

	if n.Kind != yaml.MappingNode {
		dec.add(path, "must be a mapping")

		return out
	}

	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value

		kp := join(path, k)
		if phase, ok := reserved[k]; ok {
			dec.add(kp, "is reserved for %s and not supported yet", phase)

			continue
		}

		if !contains(known, k) {
			dec.add(kp, "%s", unknown(k))

			continue
		}

		if _, dup := out[k]; dup {
			dec.add(kp, "duplicate key")

			continue
		}

		out[k] = n.Content[i+1]
	}

	return out
}

func (dec *decoder) spec(top *yaml.Node) *Spec {
	f := dec.fields("", top, nil, "version", "name", "services", "apps")

	s := &Spec{}
	if n, ok := f["version"]; ok {
		s.Version = dec.int("version", n)
	} else {
		dec.add("version", "is required")
	}

	if n, ok := f["name"]; ok {
		s.Name = dec.str("name", n)
	} else {
		dec.add("name", "is required")
	}

	if n, ok := f["services"]; ok && !isNull(n) {
		dec.each("services", n, func(name string, p string, body *yaml.Node) {
			s.Services = append(s.Services, dec.service(name, p, body))
		})
	}

	if n, ok := f["apps"]; ok && !isNull(n) {
		dec.each("apps", n, func(name string, p string, body *yaml.Node) {
			s.Apps = append(s.Apps, dec.app(name, p, body))
		})
	}

	return s
}

func (dec *decoder) each(path string, n *yaml.Node, fn func(name, path string, body *yaml.Node)) {
	if n.Kind != yaml.MappingNode {
		dec.add(path, "must be a mapping of names to definitions")

		return
	}

	seen := map[string]bool{}

	for i := 0; i+1 < len(n.Content); i += 2 {
		name := n.Content[i].Value

		p := join(path, name)
		if seen[name] {
			dec.add(p, "duplicate key")

			continue
		}

		seen[name] = true
		fn(name, p, n.Content[i+1])
	}
}

func (dec *decoder) service(name, path string, n *yaml.Node) Service {
	svc := Service{Name: name}

	if isNull(n) {
		dec.add(join(path, "type"), "is required")

		return svc
	}

	f := dec.fields(path, n, nil, "type", "resources", "config", "upstreams", "file", "static_file", "host")
	if v, ok := f["type"]; ok {
		svc.Type = dec.str(join(path, "type"), v)
	} else {
		dec.add(join(path, "type"), "is required")
	}

	if v, ok := f["resources"]; ok {
		svc.Resources = dec.resources(join(path, "resources"), v)
	}

	if v, ok := f["config"]; ok && !isNull(v) {
		svc.Config = map[string]string{}

		cp := join(path, "config")
		if v.Kind != yaml.MappingNode {
			dec.add(cp, "must be a mapping")
		} else {
			for i := 0; i+1 < len(v.Content); i += 2 {
				k := v.Content[i].Value
				svc.Config[k] = dec.str(join(cp, k), v.Content[i+1])
			}
		}
	}

	if v, ok := f["upstreams"]; ok && !isNull(v) {
		up := join(path, "upstreams")
		if v.Kind != yaml.SequenceNode {
			dec.add(up, "must be a list")
		} else {
			for i, item := range v.Content {
				svc.Upstreams = append(svc.Upstreams, dec.upstream(fmt.Sprintf("%s[%d]", up, i), item))
			}
		}
	}

	if v, ok := f["file"]; ok {
		svc.File = dec.str(join(path, "file"), v)
	}

	if v, ok := f["static_file"]; ok {
		svc.StaticFile = dec.str(join(path, "static_file"), v)
	}

	if v, ok := f["host"]; ok && !isNull(v) {
		svc.Host = dec.host(join(path, "host"), v)
	}

	return svc
}

// host accepts true, false (no host access, as for transparent), or a
// mapping with port. Validation refuses port on infra services.
func (dec *decoder) host(path string, n *yaml.Node) *Host {
	if n.Kind == yaml.ScalarNode {
		switch n.Value {
		case "true":
			return &Host{}
		case "false":
			return nil
		}

		dec.add(path, "must be true, false, or a mapping with port")

		return nil
	}

	f := dec.fields(path, n, nil, "port")

	h := &Host{}
	if v, ok := f["port"]; ok {
		h.Port = dec.positive(join(path, "port"), v, "must be between 1 and 65535")
	}

	return h
}

// upstream decodes one upstreams entry: a target name (short form) or a
// mapping with service, port, name and tls.
func (dec *decoder) upstream(path string, n *yaml.Node) Upstream {
	if n.Kind == yaml.ScalarNode {
		return Upstream{Service: dec.str(path, n)}
	}

	f := dec.fields(path, n, nil, "service", "port", "name", "tls")

	u := Upstream{}
	if n.Kind != yaml.MappingNode {
		return u
	}

	if v, ok := f["service"]; ok {
		u.Service = dec.str(join(path, "service"), v)
	} else {
		dec.add(join(path, "service"), "is required")
	}

	if v, ok := f["port"]; ok {
		u.Port = dec.positive(join(path, "port"), v, "must be between 1 and 65535")
	}

	if v, ok := f["name"]; ok {
		u.Name = dec.str(join(path, "name"), v)
	}

	if v, ok := f["tls"]; ok && !isNull(v) {
		tp := join(path, "tls")

		tf := dec.fields(tp, v, reservedTLS, "mode", "reason")
		if m, ok := tf["mode"]; ok {
			if m.Kind == yaml.ScalarNode && m.Value == TLSPassthrough {
				u.TLS.Mode = TLSPassthrough
			} else {
				u.TLS.Mode = dec.tlsModeOf(join(tp, "mode"), m, "must be on, off or passthrough")
			}
		}

		if r, ok := tf["reason"]; ok {
			u.TLS.Reason = dec.str(join(tp, "reason"), r)
		}
	}

	return u
}

func (dec *decoder) app(name, path string, n *yaml.Node) App {
	app := App{Name: name}

	if isNull(n) {
		dec.add(join(path, "path"), "is required")

		return app
	}

	f := dec.fields(path, n, nil, "path", "command", "uses", "ports", "health", "register", "resources", "replicas", "sidecars", "host")
	if v, ok := f["path"]; ok {
		app.Path = dec.str(join(path, "path"), v)
	} else {
		dec.add(join(path, "path"), "is required")
	}

	if v, ok := f["command"]; ok {
		app.Command = dec.strings(join(path, "command"), v)
	}

	if v, ok := f["uses"]; ok && !isNull(v) {
		up := join(path, "uses")
		if v.Kind != yaml.SequenceNode {
			dec.add(up, "must be a list")
		} else {
			for i, item := range v.Content {
				app.Uses = append(app.Uses, dec.use(fmt.Sprintf("%s[%d]", up, i), item))
			}
		}
	}

	if v, ok := f["ports"]; ok && !isNull(v) {
		pp := join(path, "ports")
		if v.Kind != yaml.SequenceNode {
			dec.add(pp, "must be a list of port numbers")
		} else {
			for i, item := range v.Content {
				app.Ports = append(app.Ports, dec.int(fmt.Sprintf("%s[%d]", pp, i), item))
			}
		}
	}

	if v, ok := f["health"]; ok && !isNull(v) {
		hp := join(path, "health")
		hf := dec.fields(hp, v, nil, "path", "port")

		h := &Health{}
		if p, ok := hf["path"]; ok {
			h.Path = dec.str(join(hp, "path"), p)
		}

		if p, ok := hf["port"]; ok {
			h.Port = dec.int(join(hp, "port"), p)
		}

		app.Health = h
	}

	if v, ok := f["register"]; ok && !isNull(v) {
		rp := join(path, "register")
		rf := dec.fields(rp, v, nil, "name", "port", "check", "tags")

		r := &Register{}
		if p, ok := rf["name"]; ok {
			r.Name = dec.str(join(rp, "name"), p)
		}

		if p, ok := rf["port"]; ok {
			r.Port = dec.int(join(rp, "port"), p)
		} else {
			dec.add(join(rp, "port"), "is required")
		}

		if p, ok := rf["check"]; ok {
			r.Check = dec.str(join(rp, "check"), p)
		}

		if p, ok := rf["tags"]; ok && !isNull(p) {
			r.Tags = dec.strings(join(rp, "tags"), p)
		}

		app.Register = r
	}

	if v, ok := f["resources"]; ok {
		app.Resources = dec.resources(join(path, "resources"), v)
	}

	if v, ok := f["replicas"]; ok {
		app.Replicas = dec.positive(join(path, "replicas"), v, fmt.Sprintf("must be between 1 and %d", MaxReplicas))
	}

	if v, ok := f["sidecars"]; ok && !isNull(v) {
		sp := join(path, "sidecars")
		if v.Kind != yaml.SequenceNode {
			dec.add(sp, "must be a list")
		} else {
			for i, item := range v.Content {
				app.Sidecars = append(app.Sidecars, dec.sidecar(fmt.Sprintf("%s[%d]", sp, i), item))
			}
		}
	}

	if v, ok := f["host"]; ok && !isNull(v) {
		app.Host = dec.host(join(path, "host"), v)
	}

	return app
}

func (dec *decoder) use(path string, n *yaml.Node) Use {
	if n.Kind == yaml.ScalarNode {
		return Use{Service: dec.str(path, n)}
	}

	f := dec.fields(path, n, nil, "service", "tls")

	u := Use{}
	if v, ok := f["service"]; ok {
		u.Service = dec.str(join(path, "service"), v)
	} else {
		dec.add(join(path, "service"), "is required")
	}

	if v, ok := f["tls"]; ok && !isNull(v) {
		tp := join(path, "tls")

		tf := dec.fields(tp, v, reservedTLS, "mode", "reason")
		if m, ok := tf["mode"]; ok {
			u.TLS.Mode = dec.tlsMode(join(tp, "mode"), m)
		}

		if r, ok := tf["reason"]; ok {
			u.TLS.Reason = dec.str(join(tp, "reason"), r)
		}
	}

	return u
}

// tlsMode accepts "on"/"off". YAML 1.1 readers treat bare on/off as booleans,
// so both spellings are accepted and normalised.
func (dec *decoder) tlsMode(path string, n *yaml.Node) string {
	return dec.tlsModeOf(path, n, "must be on or off")
}

func (dec *decoder) tlsModeOf(path string, n *yaml.Node, msg string) string {
	if n.Kind != yaml.ScalarNode {
		dec.add(path, "%s", msg)

		return ""
	}

	switch n.Value {
	case "on", "true":
		return TLSOn
	case "off", "false":
		return TLSOff
	}

	dec.add(path, "%s, not %q", msg, n.Value)

	return ""
}

func (dec *decoder) resources(path string, n *yaml.Node) Resources {
	var r Resources
	if isNull(n) {
		return r
	}

	f := dec.fields(path, n, nil, "memory", "cpus")
	if v, ok := f["memory"]; ok {
		r.Memory = dec.str(join(path, "memory"), v)
	}

	if v, ok := f["cpus"]; ok {
		r.CPUs = dec.str(join(path, "cpus"), v)
	}

	return r
}

func (dec *decoder) str(path string, n *yaml.Node) string {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		dec.add(path, "must be a string")

		return ""
	}

	return n.Value
}

func (dec *decoder) int(path string, n *yaml.Node) int {
	if n.Kind != yaml.ScalarNode {
		dec.add(path, "must be an integer")

		return 0
	}

	v, err := strconv.Atoi(n.Value)
	if err != nil {
		dec.add(path, "must be an integer, not %q", n.Value)

		return 0
	}

	return v
}

func (dec *decoder) strings(path string, n *yaml.Node) []string {
	if n.Kind != yaml.SequenceNode {
		dec.add(path, "must be a list of strings")

		return nil
	}

	out := make([]string, 0, len(n.Content))
	for i, item := range n.Content {
		out = append(out, dec.str(fmt.Sprintf("%s[%d]", path, i), item))
	}

	return out
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func join(path, key string) string {
	if path == "" {
		return key
	}

	return path + "." + key
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}

	return false
}

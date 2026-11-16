package spec

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// sidecarKeys lists each declarable type's keys besides type and name.
var sidecarKeys = map[string][]string{
	SidecarEnvoy:     {"inbound", "outbound", "transparent", "file"},
	SidecarPgBouncer: {"service", "pool_mode", "port", "pool_size"},
	SidecarFluentBit: {"to", "bucket"},
}

// sidecar decodes one sidecars entry strictly: the keys allowed depend on
// its type, so the type is read first.
func (dec *decoder) sidecar(path string, n *yaml.Node) Sidecar {
	var sc Sidecar
	if n.Kind != yaml.MappingNode {
		dec.add(path, "must be a mapping with a type")
		return sc
	}
	var typeNode *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "type" {
			typeNode = n.Content[i+1]
			break
		}
	}
	tp := join(path, "type")
	if typeNode == nil {
		dec.add(tp, "is required")
		return sc
	}
	sc.Type = dec.str(tp, typeNode)
	keys, ok := sidecarKeys[sc.Type]
	switch {
	case sc.Type == "":
		return sc
	case sc.Type == SidecarConsulAgent:
		dec.add(tp, "consul-agent is internal; use register to run it")
		return sc
	case !ok:
		dec.add(tp, "unknown sidecar type %q (known: %s)", sc.Type, strings.Join(SidecarTypes, ", "))
		return sc
	}
	unknown := func(k string) string {
		if k == "transparent" {
			return "is only supported on envoy sidecars"
		}
		return fmt.Sprintf("unknown key for a %s sidecar", sc.Type)
	}
	f := dec.fieldsMsg(path, n, nil, unknown, append([]string{"type", "name"}, keys...)...)
	if v, ok := f["name"]; ok {
		sc.Name = dec.str(join(path, "name"), v)
	}
	switch sc.Type {
	case SidecarEnvoy:
		sc.Envoy = dec.envoy(path, f)
	case SidecarPgBouncer:
		p := &PgBouncerSidecar{}
		if v, ok := f["service"]; ok {
			p.Service = dec.str(join(path, "service"), v)
		} else {
			dec.add(join(path, "service"), "is required")
		}
		if v, ok := f["pool_mode"]; ok {
			p.PoolMode = dec.str(join(path, "pool_mode"), v)
		}
		if v, ok := f["port"]; ok {
			p.Port = dec.positive(join(path, "port"), v, "must be between 1 and 65535")
		}
		if v, ok := f["pool_size"]; ok {
			p.PoolSize = dec.positive(join(path, "pool_size"), v, "must be between 1 and 1000")
		}
		sc.PgBouncer = p
	case SidecarFluentBit:
		fb := &FluentBitSidecar{}
		if v, ok := f["to"]; ok {
			fb.To = dec.str(join(path, "to"), v)
		} else {
			dec.add(join(path, "to"), "is required")
		}
		if v, ok := f["bucket"]; ok {
			fb.Bucket = dec.str(join(path, "bucket"), v)
		} else {
			dec.add(join(path, "bucket"), "is required")
		}
		sc.FluentBit = fb
	}
	return sc
}

func (dec *decoder) envoy(path string, f map[string]*yaml.Node) *EnvoySidecar {
	e := &EnvoySidecar{}
	if v, ok := f["inbound"]; ok && !isNull(v) {
		ip := join(path, "inbound")
		in := dec.fields(ip, v, nil, "port", "app_port")
		e.Inbound = &EnvoyInbound{}
		if p, ok := in["port"]; ok {
			e.Inbound.Port = dec.int(join(ip, "port"), p)
		} else {
			dec.add(join(ip, "port"), "is required")
		}
		if p, ok := in["app_port"]; ok {
			e.Inbound.AppPort = dec.int(join(ip, "app_port"), p)
		} else {
			dec.add(join(ip, "app_port"), "is required")
		}
	}
	if v, ok := f["outbound"]; ok && !isNull(v) {
		op := join(path, "outbound")
		if v.Kind != yaml.SequenceNode {
			dec.add(op, "must be a list")
		} else {
			for i, item := range v.Content {
				e.Outbound = append(e.Outbound, dec.outbound(fmt.Sprintf("%s[%d]", op, i), item))
			}
		}
	}
	if v, ok := f["transparent"]; ok && !isNull(v) {
		e.Transparent = dec.transparent(join(path, "transparent"), v)
	}
	if v, ok := f["file"]; ok {
		e.File = dec.str(join(path, "file"), v)
	}
	return e
}

func (dec *decoder) outbound(path string, n *yaml.Node) EnvoyOutbound {
	var o EnvoyOutbound
	f := dec.fields(path, n, nil, "service", "port", "lb", "timeout", "retries", "outlier")
	if n.Kind != yaml.MappingNode {
		return o
	}
	if v, ok := f["service"]; ok {
		o.Service = dec.str(join(path, "service"), v)
	} else {
		dec.add(join(path, "service"), "is required")
	}
	if v, ok := f["port"]; ok {
		o.Port = dec.int(join(path, "port"), v)
	} else {
		dec.add(join(path, "port"), "is required")
	}
	if v, ok := f["lb"]; ok {
		o.LB = dec.str(join(path, "lb"), v)
	}
	if v, ok := f["timeout"]; ok {
		o.Timeout = dec.str(join(path, "timeout"), v)
	}
	if v, ok := f["retries"]; ok {
		r := dec.int(join(path, "retries"), v)
		o.Retries = &r
	}
	if v, ok := f["outlier"]; ok && !isNull(v) {
		lp := join(path, "outlier")
		of := dec.fields(lp, v, nil, "consecutive_5xx", "ejection", "max_ejection_percent", "interval")
		o.Outlier = &Outlier{}
		if p, ok := of["consecutive_5xx"]; ok {
			o.Outlier.Consecutive5xx = dec.int(join(lp, "consecutive_5xx"), p)
		} else {
			dec.add(join(lp, "consecutive_5xx"), "is required")
		}
		if p, ok := of["ejection"]; ok {
			o.Outlier.Ejection = dec.str(join(lp, "ejection"), p)
		} else {
			dec.add(join(lp, "ejection"), "is required")
		}
		if p, ok := of["max_ejection_percent"]; ok {
			o.Outlier.MaxEjectionPercent = dec.positive(join(lp, "max_ejection_percent"), p, "must be between 1 and 100")
		}
		if p, ok := of["interval"]; ok {
			o.Outlier.Interval = dec.str(join(lp, "interval"), p)
		}
	}
	return o
}

// positive decodes an integer whose zero value means "use the default", so
// a written zero is reported here: validation could not tell it apart.
func (dec *decoder) positive(path string, n *yaml.Node, msg string) int {
	v := dec.int(path, n)
	if v == 0 && n.Kind == yaml.ScalarNode && n.Value == "0" {
		dec.add(path, "%s", msg)
	}
	return v
}

// transparent accepts true, false, or a mapping with egress.
func (dec *decoder) transparent(path string, n *yaml.Node) *Transparent {
	if n.Kind == yaml.ScalarNode {
		switch n.Value {
		case "true":
			return &Transparent{}
		case "false":
			return nil
		}
		dec.add(path, "must be true, false, or a mapping with egress")
		return nil
	}
	f := dec.fields(path, n, nil, "egress")
	t := &Transparent{}
	if v, ok := f["egress"]; ok {
		t.Egress = dec.str(join(path, "egress"), v)
	}
	return t
}

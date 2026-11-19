package web

import (
	"bufio"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/brian-nunez/breadboard/internal/core"
	// Only for the sidecar and upstream types core.StarterDetails exposes.
	"github.com/brian-nunez/breadboard/internal/spec"
)

// starterView is everything a starter page shows.
type starterView struct {
	Details   *core.StarterDetails
	Content   starterContent
	Readme    notes
	KeyCode   string
	Libraries []string
	Spec      string // what the starter adds to experiment.yaml
	// Configs are the native configs of the starter's proxies, in the
	// order of its services.
	Configs []starterConfig
	Graph   graph
	Catalog map[string]core.CatalogEntry
}

// starterConfig is one proxy's native config file.
type starterConfig struct {
	Service string
	Type    string
	Path    string // relative to the starter's directory
	Data    string
}

func (s *Server) startersList(w http.ResponseWriter, r *http.Request) {
	render(w, r, startersPage(s.docsData(r)))
}

// home is the landing page, at / and /home: what to build, or skip to the
// dashboard.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	render(w, r, landingPage(s.docsData(r)))
}

func (s *Server) starter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("area") + "/" + r.PathValue("name")

	d, err := s.eng.StarterDetails(r.Context(), core.StarterDetailsRequest{ID: id})
	if err != nil {
		http.NotFound(w, r)

		return
	}

	v := starterView{Details: d, Content: starterContents[id], Catalog: s.docsData(r).Catalog}
	for _, f := range d.Files {
		switch f.Path {
		case "README.md":
			v.Readme = parseNotes(string(f.Data))
		case "go.mod":
			v.Libraries = directRequires(string(f.Data))
		case v.Content.KeyFile:
			v.KeyCode = string(f.Data)
		}
	}

	v.Configs = starterConfigs(d)
	v.Spec = specAdditions(d)
	v.Graph = layout(starterRows(d, v.Catalog), "")
	v.Graph.Static = true
	render(w, r, starterPage(v))
}

// starterConfigs pairs each proxy that ships a file or static_file with
// its content.
func starterConfigs(d *core.StarterDetails) []starterConfig {
	var out []starterConfig

	for _, svc := range d.Services {
		for _, name := range []string{svc.File, svc.StaticFile} {
			if name == "" {
				continue
			}

			for _, f := range d.Files {
				if f.Path == name {
					out = append(out, starterConfig{Service: svc.Name, Type: svc.Type, Path: f.Path, Data: string(f.Data)})
				}
			}
		}
	}

	return out
}

// directRequires lists the module paths a go.mod requires directly.
func directRequires(gomod string) []string {
	var out []string

	inBlock := false

	sc := bufio.NewScanner(strings.NewReader(gomod))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "" && !strings.Contains(line, "// indirect"):
			out = append(out, strings.Fields(line)[0])
		case strings.HasPrefix(line, "require ") && !strings.Contains(line, "// indirect"):
			out = append(out, strings.Fields(line)[1])
		}
	}

	return out
}

// specAdditions is the experiment.yaml a fresh experiment gets from the
// starter: one service per required type, its proxies, and its apps.
func specAdditions(d *core.StarterDetails) string {
	var b strings.Builder
	b.WriteString("services:\n")

	for _, t := range d.Requires {
		fmt.Fprintf(&b, "  %s:\n    type: %s\n", t, t)
	}

	for _, svc := range d.Services {
		fmt.Fprintf(&b, "  %s:\n    type: %s\n", svc.Name, svc.Type)

		if len(svc.Config) > 0 {
			b.WriteString("    config:\n")

			for _, k := range slices.Sorted(maps.Keys(svc.Config)) {
				fmt.Fprintf(&b, "      %s: %s\n", k, svc.Config[k])
			}
		}

		writeUpstreams(&b, svc.Upstreams)

		if svc.File != "" {
			fmt.Fprintf(&b, "    file: ./%s/%s\n", d.Dir, svc.File)
		}

		if svc.StaticFile != "" {
			fmt.Fprintf(&b, "    static_file: ./%s/%s\n", d.Dir, svc.StaticFile)
		}
	}

	b.WriteString("apps:\n")

	for _, a := range d.Apps {
		fmt.Fprintf(&b, "  %s:\n    path: ./%s\n", a.Name, d.Dir)

		if len(a.Command) > 0 {
			fmt.Fprintf(&b, "    command: [%s]\n", strings.Join(a.Command, ", "))
		}

		if len(a.Uses) > 0 {
			fmt.Fprintf(&b, "    uses: [%s]\n", strings.Join(a.Uses, ", "))
		}

		if a.Register != nil {
			fmt.Fprintf(&b, "    register: {port: %d}\n", a.Register.Port)
		}

		if a.Replicas > 1 {
			fmt.Fprintf(&b, "    replicas: %d\n", a.Replicas)
		}

		if len(a.Sidecars) > 0 {
			b.WriteString("    sidecars:\n")

			for _, sc := range a.Sidecars {
				writeSidecar(&b, sc.Type, sc.Name, sidecarFields(sc.Envoy, sc.PgBouncer, sc.FluentBit))
			}
		}
	}

	return b.String()
}

// writeUpstreams writes a proxy's upstreams the way bb starter writes them:
// the short form when every entry names only its target.
func writeUpstreams(b *strings.Builder, ups []spec.Upstream) {
	if len(ups) == 0 {
		return
	}

	short := !slices.ContainsFunc(ups, func(u spec.Upstream) bool { return u.Port != 0 || u.Name != "" || u.TLS != (spec.TLS{}) })
	if short {
		names := make([]string, len(ups))
		for i, u := range ups {
			names[i] = u.Service
		}

		fmt.Fprintf(b, "    upstreams: [%s]\n", strings.Join(names, ", "))

		return
	}

	b.WriteString("    upstreams:\n")

	for _, u := range ups {
		if u.Port == 0 && u.Name == "" && u.TLS == (spec.TLS{}) {
			fmt.Fprintf(b, "      - %s\n", u.Service)

			continue
		}

		fmt.Fprintf(b, "      - service: %s\n", u.Service)

		if u.Port != 0 {
			fmt.Fprintf(b, "        port: %d\n", u.Port)
		}

		if u.Name != "" {
			fmt.Fprintf(b, "        name: %s\n", u.Name)
		}

		switch {
		case u.TLS.Reason != "":
			fmt.Fprintf(b, "        tls:\n          mode: %s\n          reason: %s\n", u.TLS.Mode, u.TLS.Reason)
		case u.TLS.Mode != "":
			fmt.Fprintf(b, "        tls: {mode: %s}\n", u.TLS.Mode)
		}
	}
}

// writeSidecar writes one sidecars entry the way bb starter writes it.
func writeSidecar(b *strings.Builder, typ, name string, fields []string) {
	fmt.Fprintf(b, "      - type: %s\n", typ)

	if name != "" {
		fmt.Fprintf(b, "        name: %s\n", name)
	}

	for _, f := range fields {
		b.WriteString("        " + f + "\n")
	}
}

// sidecarFields lists a sidecar's declared fields as YAML lines (nested
// lines carry their extra indentation). Exactly one argument is non-nil.
func sidecarFields(e *spec.EnvoySidecar, p *spec.PgBouncerSidecar, f *spec.FluentBitSidecar) []string {
	var out []string

	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	switch {
	case e != nil:
		if e.Inbound != nil {
			add("inbound: {port: %d, app_port: %d}", e.Inbound.Port, e.Inbound.AppPort)
		}

		if len(e.Outbound) > 0 {
			add("outbound:")

			for _, o := range e.Outbound {
				add("  - service: %s", o.Service)
				add("    port: %d", o.Port)

				if o.LB != "" {
					add("    lb: %s", o.LB)
				}

				if o.Timeout != "" {
					add("    timeout: %s", o.Timeout)
				}

				if o.Retries != nil {
					add("    retries: %d", *o.Retries)
				}

				if o.Outlier != nil {
					l := fmt.Sprintf("consecutive_5xx: %d, ejection: %s", o.Outlier.Consecutive5xx, o.Outlier.Ejection)
					if o.Outlier.MaxEjectionPercent != 0 {
						l += fmt.Sprintf(", max_ejection_percent: %d", o.Outlier.MaxEjectionPercent)
					}

					if o.Outlier.Interval != "" {
						l += ", interval: " + o.Outlier.Interval
					}

					add("    outlier: {%s}", l)
				}
			}
		}

		if t := e.Transparent; t != nil {
			if t.Egress == "" {
				add("transparent: true")
			} else {
				add("transparent: {egress: %s}", t.Egress)
			}
		}
	case p != nil:
		add("service: %s", p.Service)

		if p.PoolMode != "" {
			add("pool_mode: %s", p.PoolMode)
		}

		if p.PoolSize != 0 {
			add("pool_size: %d", p.PoolSize)
		}
	case f != nil:
		add("to: %s", f.To)
		add("bucket: %s", f.Bucket)
	}

	return out
}

// starterRows turns a starter into rows the architecture layout can draw.
func starterRows(d *core.StarterDetails, cat map[string]core.CatalogEntry) []row {
	var rows []row
	for _, t := range d.Requires {
		rows = append(rows, row{ServiceStatus: core.ServiceStatus{Name: t, Kind: "infra", Type: t, Health: core.HealthHealthy}, TLS: true})
	}

	proxyType := map[string]string{}
	for _, svc := range d.Services {
		proxyType[svc.Name] = svc.Type
	}

	catPort := func(name string) int {
		typ := name
		if t, ok := proxyType[name]; ok {
			typ = t
		}

		if e, ok := cat[typ]; ok && len(e.Ports) > 0 {
			return e.Ports[0]
		}

		return 0
	}

	apps := map[string]core.StarterApp{}
	for _, a := range d.Apps {
		apps[a.Name] = a
	}

	for _, svc := range d.Services {
		var ups []core.Edge

		for _, u := range svc.Upstreams {
			e := core.Edge{To: u.Service, Port: u.Port, TLS: u.Mode() != spec.TLSOff, Reason: u.TLS.Reason, Mode: u.Mode()}
			if target, ok := apps[u.Service]; ok {
				e.ToApp = true
				if e.Port == 0 {
					e.Port = starterTLSPort(target)
					if u.Mode() == spec.TLSOff {
						e.Port = spec.DefaultPlainPort
					}
				}
			} else if e.Port == 0 {
				e.Port = catPort(u.Service)
			}

			ups = append(ups, e)
		}
		// Routes from a Consul catalog: the registry, and every app that
		// registers with the tag Traefik looks for.
		if registry := svc.Config["consul_catalog"]; registry != "" {
			ups = append(ups, core.Edge{To: registry, Port: catPort(registry), TLS: true, Discovery: "registry"})

			for _, a := range d.Apps {
				if a.Register != nil && slices.Contains(a.Register.Tags, "traefik.enable=true") {
					ups = append(ups, core.Edge{To: a.Name, Port: a.Register.Port, TLS: true, ToApp: true, Discovery: "route"})
				}
			}
		}

		rows = append(rows, row{ServiceStatus: core.ServiceStatus{Name: svc.Name, Kind: "infra", Type: svc.Type, Health: core.HealthHealthy, Uses: ups}, TLS: true})
	}

	for _, a := range d.Apps {
		var uses []core.Edge
		for _, u := range a.Uses {
			uses = append(uses, core.Edge{To: u, Port: catPort(u), TLS: true})
		}

		for _, sc := range a.Sidecars {
			if sc.Envoy == nil {
				continue
			}

			for _, o := range sc.Envoy.Outbound {
				e := core.Edge{To: o.Service, TLS: true, Via: sc.ID()}
				if target, ok := apps[o.Service]; ok {
					e.ToApp = true
					e.Port = starterTLSPort(target)
				} else {
					e.Port = catPort(o.Service)
				}

				if !slices.ContainsFunc(uses, func(u core.Edge) bool { return u.To == e.To }) {
					uses = append(uses, e)
				}
			}
		}

		n := max(a.Replicas, 1)
		for i := 1; i <= n; i++ {
			name := a.Name
			st := core.ServiceStatus{Kind: "app", Health: core.HealthHealthy, Uses: uses}

			if n > 1 {
				name = fmt.Sprintf("%s-%d", a.Name, i)
				st.App, st.Instance, st.Replicas = a.Name, i, n
			}

			st.Name = name
			for _, sc := range a.Sidecars {
				st.Sidecars = append(st.Sidecars, name+"-"+sc.ID())
			}

			rows = append(rows, row{ServiceStatus: st, TLS: true})

			for _, sc := range a.Sidecars {
				info := &core.SidecarInfo{Name: sc.ID(), Service: name + "-" + sc.ID(), Type: sc.Type, Transparent: sc.Transparent()}
				rows = append(rows, row{ServiceStatus: core.ServiceStatus{Name: info.Service, Kind: "sidecar", Type: sc.Type, Health: core.HealthHealthy, Parent: name, Sidecar: info}, TLS: true})
			}

			if a.Register != nil {
				rows = append(rows, row{ServiceStatus: core.ServiceStatus{Name: name + "-agent", Kind: "sidecar", Type: "consul-agent", Health: core.HealthHealthy, Parent: name, Registry: "consul"}, TLS: true})
			}
		}
	}

	return rows
}

// starterTLSPort is the port other apps reach a starter app on over TLS:
// its Envoy sidecar's inbound port, else the port it registers.
func starterTLSPort(a core.StarterApp) int {
	for _, sc := range a.Sidecars {
		if sc.Envoy != nil && sc.Envoy.Inbound != nil {
			return sc.Envoy.Inbound.Port
		}
	}

	if a.Register != nil {
		return a.Register.Port
	}

	return 0
}

// techName names a catalog type for display.
func techName(t string, cat map[string]core.CatalogEntry) string {
	if e, ok := cat[t]; ok {
		return e.Title
	}

	return t
}

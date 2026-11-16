package web

import (
	"bufio"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/brian-nunez/breadboard/internal/core"
	// Only for the sidecar types core.StarterApp exposes.
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
	Graph     graph
	Catalog   map[string]core.CatalogEntry
}

func (s *Server) startersList(w http.ResponseWriter, r *http.Request) {
	render(w, r, startersPage(s.docsData(r), false))
}

// home is the landing page when the directory has no experiment yet: what to
// build, or skip to the (empty) dashboard.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	render(w, r, startersPage(s.docsData(r), true))
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
		switch {
		case f.Path == "README.md":
			v.Readme = parseNotes(string(f.Data))
		case f.Path == "go.mod":
			v.Libraries = directRequires(string(f.Data))
		case f.Path == v.Content.KeyFile:
			v.KeyCode = string(f.Data)
		}
	}
	v.Spec = specAdditions(d)
	v.Graph = layout(starterRows(d, v.Catalog), "")
	v.Graph.Static = true
	render(w, r, starterPage(v))
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
// starter: one service per required type, and its apps.
func specAdditions(d *core.StarterDetails) string {
	var b strings.Builder
	b.WriteString("services:\n")
	for _, t := range d.Requires {
		fmt.Fprintf(&b, "  %s:\n    type: %s\n", t, t)
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
		rows = append(rows, row{ServiceStatus: core.ServiceStatus{Name: t, Kind: "infra", Health: core.HealthHealthy}, TLS: true})
	}
	catPort := func(typ string) int {
		if e, ok := cat[typ]; ok && len(e.Ports) > 0 {
			return e.Ports[0]
		}
		return 0
	}
	apps := map[string]core.StarterApp{}
	for _, a := range d.Apps {
		apps[a.Name] = a
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
				rows = append(rows, row{ServiceStatus: core.ServiceStatus{Name: name + "-agent", Kind: "sidecar", Health: core.HealthHealthy, Parent: name, Registry: "consul"}, TLS: true})
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

// techNames names a starter's catalog types for display.
func techNames(types []string, cat map[string]core.CatalogEntry) string {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = t
		if e, ok := cat[t]; ok {
			names[i] = e.Title
		}
	}
	return strings.Join(names, " · ")
}

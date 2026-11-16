package web

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"

	"github.com/brian-nunez/breadboard/internal/core"
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
	}
	return b.String()
}

// starterRows turns a starter into rows the architecture layout can draw.
func starterRows(d *core.StarterDetails, cat map[string]core.CatalogEntry) []row {
	var rows []row
	for _, t := range d.Requires {
		rows = append(rows, row{ServiceStatus: core.ServiceStatus{Name: t, Kind: "infra", Health: core.HealthHealthy}, TLS: true})
	}
	for _, a := range d.Apps {
		r := row{ServiceStatus: core.ServiceStatus{Name: a.Name, Kind: "app", Health: core.HealthHealthy}, TLS: true}
		for _, u := range a.Uses {
			port := 0
			if e, ok := cat[u]; ok && len(e.Ports) > 0 {
				port = e.Ports[0]
			}
			r.Uses = append(r.Uses, core.Edge{To: u, Port: port, TLS: true})
		}
		rows = append(rows, r)
		if a.Register != nil {
			rows = append(rows, row{ServiceStatus: core.ServiceStatus{Name: a.Name + "-agent", Kind: "sidecar", Health: core.HealthHealthy, Parent: a.Name, Registry: "consul"}, TLS: true})
		}
	}
	return rows
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

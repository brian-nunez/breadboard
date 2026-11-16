package web

import (
	"cmp"
	"fmt"
	"slices"
)

// Graph geometry, in SVG user units.
const (
	nodeW     = 176
	nodeH     = 56
	colGap    = 112
	rowGap    = 40
	headerH   = 40
	graphPad  = 24
	sidecarDy = 16
)

// graph is the laid-out architecture: your code on the left, infrastructure
// on the right, each infrastructure node placed near the apps that use it so
// edges cross as little as possible.
type graph struct {
	Width, Height int
	Nodes         []node
	Edges         []edge
	CodeX, InfraX int // column header positions
}

type node struct {
	row
	X, Y     int
	Selected bool
}

type edge struct {
	Path        string // SVG path data
	LabelX      int
	LabelY      int
	Label       string
	Plaintext   bool
	Reason      string
	Registering bool // a sidecar registering its app
}

func (n node) CX() int { return n.X + nodeW }
func (n node) CY() int { return n.Y + nodeH/2 }

// layout positions every service. Apps (with their sidecars just below them)
// fill the left column in experiment order; each infra service gets the
// average row of the apps that use it, and unused ones go last.
func layout(rows []row, selected string) graph {
	g := graph{CodeX: graphPad, InfraX: graphPad + nodeW + colGap}
	var apps, infra []row
	for _, r := range rows {
		if r.Kind == "infra" {
			infra = append(infra, r)
		} else {
			apps = append(apps, r)
		}
	}
	top := graphPad + headerH
	pos := map[string]node{}
	y := top
	for _, a := range apps {
		n := node{row: a, X: g.CodeX, Y: y, Selected: a.Name == selected}
		if a.Kind == "sidecar" {
			n.Y -= rowGap - sidecarDy // tucked under its app
			y -= rowGap - sidecarDy
		}
		pos[a.Name] = n
		g.Nodes = append(g.Nodes, n)
		y += nodeH + rowGap
	}
	// Barycentre of the rows of the apps each infra service is used by.
	weight := map[string]float64{}
	for _, r := range infra {
		var sum, n float64
		for _, a := range apps {
			for _, e := range a.Uses {
				if e.To == r.Name {
					sum += float64(pos[a.Name].Y)
					n++
				}
			}
			if a.Registry == r.Name {
				sum += float64(pos[a.Name].Y)
				n++
			}
		}
		weight[r.Name] = 1e9 // unused: last
		if n > 0 {
			weight[r.Name] = sum / n
		}
	}
	slices.SortStableFunc(infra, func(a, b row) int { return cmp.Compare(weight[a.Name], weight[b.Name]) })
	iy := top
	for _, r := range infra {
		want := int(weight[r.Name])
		if want > iy && want < 1e9 {
			iy = want
		}
		n := node{row: r, X: g.InfraX, Y: iy, Selected: r.Name == selected}
		pos[r.Name] = n
		g.Nodes = append(g.Nodes, n)
		iy += nodeH + rowGap
	}
	for _, a := range apps {
		from := pos[a.Name]
		for _, e := range a.Uses {
			to, ok := pos[e.To]
			if !ok {
				continue
			}
			g.Edges = append(g.Edges, curve(from, to, edgeLabel(e.TLS, e.Port), !e.TLS, e.Reason, false))
		}
		if a.Registry != "" {
			if to, ok := pos[a.Registry]; ok {
				g.Edges = append(g.Edges, curve(from, to, "registers", false, "", true))
			}
		}
	}
	g.Width = g.InfraX + nodeW + graphPad
	for _, n := range g.Nodes {
		g.Height = max(g.Height, n.Y+nodeH+graphPad)
	}
	return g
}

func edgeLabel(tls bool, port int) string {
	if tls {
		return fmt.Sprintf("TLS :%d", port)
	}
	return fmt.Sprintf("plaintext :%d", port)
}

// curve is a horizontal S-curve from the right edge of one node to the left
// edge of another, with its label at the middle.
func curve(from, to node, label string, plaintext bool, reason string, registering bool) edge {
	x1, y1 := from.CX(), from.CY()
	x2, y2 := to.X-6, to.CY() // stop short for the arrowhead
	mid := (x1 + x2) / 2
	return edge{
		Path:        fmt.Sprintf("M%d %d C%d %d, %d %d, %d %d", x1, y1, mid, y1, mid, y2, x2, y2),
		LabelX:      mid,
		LabelY:      (y1 + y2) / 2,
		Label:       label,
		Plaintext:   plaintext,
		Reason:      reason,
		Registering: registering,
	}
}

// inbound lists the services that connect to name.
func inbound(rows []row, name string) []string {
	var out []string
	for _, r := range rows {
		for _, e := range r.Uses {
			if e.To == name {
				out = append(out, r.Name)
			}
		}
		if r.Registry == name {
			out = append(out, r.Name)
		}
	}
	return out
}

func labelWidth(s string) int { return 8 + 6*len(s) }

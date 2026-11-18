package web

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Graph geometry, in SVG user units.
const (
	nodeW    = 176
	nodeH    = 56
	colGap   = 112
	rowGap   = 40
	headerH  = 40
	graphPad = 24
	// Sidecar chips sit in rows under their app's card, inside the pod
	// outline.
	chipH   = 20
	chipGap = 6
	podPad  = 6
	// appGutter is the room left of the code column for app-to-app edges.
	appGutter = 104
	// stackDx is the offset of each card drawn behind a replicated app.
	stackDx = 5
)

// graph is the laid-out architecture: your code on the left, infrastructure
// on the right, each infrastructure node placed near the apps that use it so
// edges cross as little as possible.
type graph struct {
	Width, Height int
	Nodes         []node
	Edges         []edge
	CodeX, InfraX int // the code and infrastructure columns
	// Columns are the headed columns, left to right.
	Columns []column
	// Static graphs (starter pages) have no live nodes to select.
	Static bool
}

// node is one card: an infra service, or an app drawn once however many
// replicas it runs, with its sidecars as chips inside its pod outline.
type node struct {
	row          // the service, or an app's first instance
	X, Y     int // the card's top left
	H        int // the card and its chips
	Selected bool
	// Instances are an app's replicas (more than one means a stacked card).
	Instances []row
	Chips     []chip
}

// chip is a sidecar drawn inside its app's pod outline.
type chip struct {
	Name        string // the first instance's sidecar service, for links
	Type        string // the sidecar type, for its logo
	Label       string
	Color       string // status colour: the worst over the app's instances
	Transparent bool
	Selected    bool
	X, Y, W     int
}

// column is a headed column of the graph.
type column struct {
	Title string
	X     int
}

type edge struct {
	Path        string // SVG path data
	LabelX      int
	LabelY      int
	Label       string
	Plaintext   bool
	Reason      string
	Registering bool // a sidecar registering its app
	ToApp       bool // app to app, through an Envoy sidecar
	// fixedLabel keeps the label where the edge put it (in the gutter).
	fixedLabel bool
}

func (n node) CX() int { return n.X + nodeW }
func (n node) CY() int { return n.Y + nodeH/2 }

// Replicas is how many instances the card stands for.
func (n node) Replicas() int { return max(len(n.Instances), 1) }

// Stack lists the offsets of the cards drawn behind a replicated app.
func (n node) Stack() []int {
	var out []int
	for i := min(n.Replicas()-1, 2); i >= 1; i-- {
		out = append(out, i*stackDx)
	}
	return out
}

// Pod reports whether the app has sidecars (an outline around card and chips).
func (n node) Pod() bool { return len(n.Chips) > 0 }

// Label is the name on the card: the app's name for a replicated app.
func (n node) Label() string {
	if n.App != "" && len(n.Instances) > 1 {
		return n.App
	}
	return n.Name
}

// InstanceDot is the position of instance i's status dot on a stacked card.
func (n node) InstanceDot(i int) int { return n.X + 54 + 4 + i*12 }

// appGroup is an app with its instances and each instance's sidecars.
type appGroup struct {
	name      string
	instances []row
	sidecars  map[string][]row // by parent instance
}

// layout positions every service. Apps fill the left column in experiment
// order, one card per app with its sidecars as chips; each infra service
// gets the average row of the apps that use it, and unused ones go last.
func layout(rows []row, selected string) graph {
	if hasProxies(rows) {
		return flowLayout(rows, selected)
	}
	groups, infra, sidecars := groupRows(rows)
	return layoutTwoColumns(selected, groups, infra, sidecars)
}

// groupRows sorts rows into apps (with their instances), infra services,
// and sidecars by parent instance.
func groupRows(rows []row) (groups []*appGroup, infra []row, sidecars map[string][]row) {
	byGroup := map[string]*appGroup{}
	sidecars = map[string][]row{}
	for _, r := range rows {
		switch r.Kind {
		case "infra":
			infra = append(infra, r)
		case "sidecar":
			sidecars[r.Parent] = append(sidecars[r.Parent], r)
		default:
			name := cmp.Or(r.App, r.Name)
			g, ok := byGroup[name]
			if !ok {
				g = &appGroup{name: name}
				byGroup[name] = g
				groups = append(groups, g)
			}
			g.instances = append(g.instances, r)
		}
	}
	return groups, infra, sidecars
}

// layoutTwoColumns is the layout without proxies: your code, then
// infrastructure.
func layoutTwoColumns(selected string, groups []*appGroup, infra []row, sidecars map[string][]row) graph {
	appEdges := false
	for _, g := range groups {
		for _, e := range g.instances[0].Uses {
			appEdges = appEdges || e.ToApp
		}
	}
	codeX := graphPad
	if appEdges {
		codeX += appGutter
	}
	g := graph{CodeX: codeX, InfraX: codeX + nodeW + colGap}
	g.Columns = []column{{"YOUR CODE", g.CodeX}, {"INFRASTRUCTURE", g.InfraX}}
	top := graphPad + headerH
	pos := map[string]node{} // by app, instance, and infra name
	y := top
	var apps []node
	for _, grp := range groups {
		first := grp.instances[0]
		n := node{row: first, X: g.CodeX, Y: y, H: nodeH}
		if len(grp.instances) > 1 {
			n.Instances = grp.instances
		}
		for _, inst := range grp.instances {
			n.Selected = n.Selected || inst.Name == selected
		}
		n.Chips = chips(n, grp.instances, sidecars, selected)
		if len(n.Chips) > 0 {
			last := n.Chips[len(n.Chips)-1]
			n.H = last.Y + chipH - n.Y
		}
		pos[grp.name] = n
		for _, inst := range grp.instances {
			pos[inst.Name] = n
		}
		apps = append(apps, n)
		g.Nodes = append(g.Nodes, n)
		y += n.H + rowGap
		if n.Pod() {
			y += 2 * podPad
		}
	}
	// Barycentre of the rows of the apps each infra service is used by.
	weight := map[string]float64{}
	for _, r := range infra {
		var sum, cnt float64
		for _, a := range apps {
			uses := slices.ContainsFunc(a.Uses, func(e core.Edge) bool { return e.To == r.Name })
			registers := slices.ContainsFunc(sidecars[a.Name], func(s row) bool { return s.Registry == r.Name })
			if uses || registers {
				sum += float64(a.Y)
				cnt++
			}
		}
		weight[r.Name] = 1e9 // unused: last
		if cnt > 0 {
			weight[r.Name] = sum / cnt
		}
	}
	slices.SortStableFunc(infra, func(a, b row) int { return cmp.Compare(weight[a.Name], weight[b.Name]) })
	iy := top
	for _, r := range infra {
		want := int(weight[r.Name])
		if want > iy && want < 1e9 {
			iy = want
		}
		n := node{row: r, X: g.InfraX, Y: iy, H: nodeH, Selected: r.Name == selected}
		pos[r.Name] = n
		g.Nodes = append(g.Nodes, n)
		iy += nodeH + rowGap
	}
	for _, a := range apps {
		for _, e := range a.Uses {
			to, ok := pos[e.To]
			if !ok {
				continue
			}
			if e.ToApp {
				g.Edges = append(g.Edges, loop(a, to, edgeLabel(e.TLS, e.Port)))
				continue
			}
			g.Edges = append(g.Edges, curve(a.CX(), a.CY(), to, edgeLabel(e.TLS, e.Port), !e.TLS, e.Reason, false))
		}
		for _, c := range a.Chips {
			for _, s := range sidecars[a.Name] {
				if s.Name != c.Name || s.Registry == "" {
					continue
				}
				if to, ok := pos[s.Registry]; ok {
					g.Edges = append(g.Edges, curve(a.CX()+podPad, c.Y+chipH/2, to, "registers", false, "", true))
				}
			}
		}
	}
	g.Width = g.InfraX + nodeW + graphPad
	for _, n := range g.Nodes {
		g.Height = max(g.Height, n.Y+n.H+podPad+graphPad)
	}
	return g
}

// chips lays out the first instance's sidecars under the card, wrapping to
// new rows. A chip's colour is the worst status of that sidecar over every
// instance.
func chips(n node, instances []row, sidecars map[string][]row, selected string) []chip {
	var out []chip
	x, y := n.X, n.Y+nodeH+chipGap
	for i, s := range sidecars[instances[0].Name] {
		c := chip{Name: s.Name, Type: s.Type, Label: s.Type, Color: s.Status().Color}
		if s.Sidecar != nil && s.Sidecar.Transparent {
			c.Transparent = true
			c.Label += " · transparent"
		}
		for _, inst := range instances {
			list := sidecars[inst.Name]
			if i < len(list) {
				c.Selected = c.Selected || list[i].Name == selected
				c.Color = worse(c.Color, list[i].Status().Color)
			}
		}
		c.W = labelWidth(c.Label) + 14 + chipLogoW(c) // room for the status dot and logo
		if x > n.X && x+c.W > n.X+nodeW {
			x, y = n.X, y+chipH+chipGap
		}
		c.X, c.Y = x, y
		out = append(out, c)
		x += c.W + chipGap
	}
	return out
}

// chipLogoW is the room a chip's logo takes before its label.
func chipLogoW(c chip) int {
	if logoSrc(c.Type) != "" {
		return 16
	}
	return 0
}

// worse is the more alarming of two status colours.
func worse(a, b string) string {
	rank := map[string]int{"green": 0, "grey": 1, "blue": 2, "amber": 3, "red": 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func edgeLabel(tls bool, port int) string {
	if tls {
		return fmt.Sprintf("TLS :%d", port)
	}
	return fmt.Sprintf("plaintext :%d", port)
}

// curve is a horizontal S-curve from a point on the right of an app to the
// left edge of another node, with its label at the middle.
func curve(x1, y1 int, to node, label string, plaintext bool, reason string, registering bool) edge {
	x2, y2 := to.X-6, to.CY() // stop short for the arrowhead
	if to.Pod() {
		x2 -= podPad
	}
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

// loop is an app-to-app edge: out of the left side of one app card, round
// through the gutter, into the left side of the other.
func loop(from, to node, label string) edge {
	x1, y1 := from.X, from.CY()
	if from.Pod() {
		x1 -= podPad
	}
	x2, y2 := to.X-6, to.CY()
	if to.Pod() {
		x2 -= podPad
	}
	bend := graphPad + 8
	return edge{
		Path:   fmt.Sprintf("M%d %d C%d %d, %d %d, %d %d", x1, y1, bend, y1, bend, y2, x2, y2),
		LabelX: bend + (x1-bend)/4 + 8,
		LabelY: (y1 + y2) / 2,
		Label:  label,
		ToApp:  true,
	}
}

// inbound lists the services that connect to r: by its name, or for an app
// instance by its app's name (an Envoy outbound target is the whole app).
func inbound(rows []row, r row) []string {
	var out []string
	for _, from := range rows {
		if from.Instance > 1 {
			continue // an app is listed once
		}
		for _, e := range from.Uses {
			if e.To == r.Name || (r.App != "" && e.To == r.App) {
				out = append(out, cmp.Or(from.App, from.Name))
			}
		}
		if from.Registry != "" && from.Registry == r.Name {
			out = append(out, from.Name)
		}
	}
	return out
}

func labelWidth(s string) int { return 8 + 6*len(s) }

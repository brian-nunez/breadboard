package web

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/brian-nunez/breadboard/internal/core"
)

// The flow layout draws an experiment with proxies in up to four columns
// that follow the traffic, left to right: your code that no proxy sends
// to (the clients), the proxies, the apps behind proxies, and the other
// infrastructure. Empty columns are left out. An edge that runs against
// the flow (an app behind a proxy that also uses it) leaves its card on
// the left and enters the target on the right.

const (
	// sideBend is how far a same-column edge swings out to the right.
	sideBend = 48
	// flowGap is the gap after the proxy column: a proxy's edges fan out
	// to several backends and carry wider labels (passthrough :8443).
	flowGap = 176
)

// hasProxies reports whether any service has upstream edges.
func hasProxies(rows []row) bool {
	return slices.ContainsFunc(rows, isProxy)
}

// isProxy reports whether a row is a proxy with upstream edges.
func isProxy(r row) bool {
	return r.Kind == "infra" && slices.ContainsFunc(r.Uses, func(e core.Edge) bool { return e.Mode != "" })
}

func flowLayout(rows []row, selected string) graph {
	groups, infra, sidecars := groupRows(rows)

	isApp := map[string]bool{}
	for _, grp := range groups {
		isApp[grp.name] = true
	}

	var proxies, others []row

	behind := map[string]bool{} // apps a proxy sends to

	for _, r := range infra {
		if !isProxy(r) {
			others = append(others, r)

			continue
		}

		proxies = append(proxies, r)
		for _, e := range r.Uses {
			if isApp[e.To] {
				behind[e.To] = true
			}
		}
	}

	var clients, backends []*appGroup

	for _, grp := range groups {
		if behind[grp.name] {
			backends = append(backends, grp)
		} else {
			clients = append(clients, grp)
		}
	}

	// Columns, left to right, skipping empty ones. App-to-app edges within
	// the first column loop through a gutter on its left.
	x := graphPad
	if len(clients) > 0 && sameColumnAppEdges(clients) {
		x += appGutter
	}

	g := graph{}

	var clientX, proxyX, backendX, infraX int

	next := func(title string, n int) int {
		if n == 0 {
			return -1
		}

		at := x
		g.Columns = append(g.Columns, column{title, at})

		x += nodeW + colGap
		if title == "PROXIES" {
			x += flowGap - colGap
		}

		return at
	}
	clientX = next("YOUR CODE", len(clients))
	proxyX = next("PROXIES", len(proxies))
	backendX = next("BEHIND PROXIES", len(backends))
	infraX = next("INFRASTRUCTURE", len(others))
	g.CodeX, g.InfraX = g.Columns[0].X, g.Columns[len(g.Columns)-1].X

	top := graphPad + headerH
	pos := map[string]node{}

	var apps []node // every app card, for edges

	placeApps := func(list []*appGroup, colX int, want func(*appGroup) int) {
		y := top
		for _, grp := range list {
			if w := want(grp); w > y {
				y = w
			}

			n := appNode(grp, colX, y, sidecars, selected)

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
	}
	// placeCards stacks single cards, each at the average row of the
	// placed nodes with an edge to it; cards nothing reaches go last.
	placeCards := func(list []row, colX int) {
		weight := map[string]float64{}

		for _, r := range list {
			var sum, cnt float64

			for name, n := range pos {
				if name != n.Name {
					continue // each card once
				}

				reaches := slices.ContainsFunc(n.Uses, func(e core.Edge) bool { return e.To == r.Name })
				for _, c := range n.Chips {
					reaches = reaches || slices.ContainsFunc(sidecars[n.Name], func(s row) bool { return s.Name == c.Name && s.Registry == r.Name })
				}

				if reaches {
					sum += float64(n.Y)
					cnt++
				}
			}

			weight[r.Name] = 1e9
			if cnt > 0 {
				weight[r.Name] = sum / cnt
			}
		}

		slices.SortStableFunc(list, func(a, b row) int { return cmp.Compare(weight[a.Name], weight[b.Name]) })

		y := top
		for _, r := range list {
			if w := int(weight[r.Name]); w > y && w < 1e9 {
				y = w
			}

			n := node{row: r, X: colX, Y: y, H: nodeH, Selected: r.Name == selected}
			pos[r.Name] = n
			g.Nodes = append(g.Nodes, n)
			y += nodeH + rowGap
		}
	}

	placeApps(clients, clientX, func(*appGroup) int { return 0 })
	placeCards(proxies, proxyX)
	placeApps(backends, backendX, func(grp *appGroup) int {
		var sum, cnt int

		for _, p := range proxies {
			if slices.ContainsFunc(p.Uses, func(e core.Edge) bool { return e.To == grp.name }) {
				sum += pos[p.Name].Y
				cnt++
			}
		}

		if cnt == 0 {
			return 0
		}

		return sum / cnt
	})
	placeCards(others, infraX)

	// Edges: every app card's (its first instance's) and every proxy's.
	from := slices.Clone(apps)
	for _, p := range proxies {
		from = append(from, pos[p.Name])
	}

	for _, a := range from {
		for _, e := range a.Uses {
			to, ok := pos[e.To]
			if !ok {
				continue
			}

			g.Edges = append(g.Edges, flowEdge(a, to, e, clientX))
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

	spreadLabels(g.Edges, g.Nodes)
	last := g.Columns[len(g.Columns)-1].X

	g.Width = last + nodeW + graphPad
	for _, n := range g.Nodes {
		g.Height = max(g.Height, n.Y+n.H+podPad+graphPad)
	}

	return g
}

// box is a label's or card's rectangle.
type box struct{ x1, y1, x2, y2 int }

func (a box) overlap(b box) int {
	w, h := min(a.x2, b.x2)-max(a.x1, b.x1), min(a.y2, b.y2)-max(a.y1, b.y1)
	if w <= 0 || h <= 0 {
		return 0
	}

	return w * h
}

// labelTs are the points along an edge, from its middle outwards, where
// its label may sit.
var labelTs = []float64{0.5, 0.4, 0.6, 0.3, 0.7, 0.22, 0.78}

// spreadLabels slides each edge's label along its curve to the first spot
// that covers no card and no label placed before it (the least covered
// spot when none is free). Several proxies in front of the same apps
// would otherwise stack their labels in one place.
func spreadLabels(edges []edge, nodes []node) {
	var taken []box

	for _, n := range nodes {
		top := n.Y - stackTop(n)
		taken = append(taken, box{n.X, top, n.X + nodeW + stackTop(n), n.Y + n.H})
	}

	labelBox := func(x, y int, label string) box {
		w := labelWidth(label)

		return box{x - w/2 - 2, y - 11, x + w/2 + 2, y + 11}
	}

	for i := range edges {
		e := &edges[i]

		var p [8]float64

		_, err := fmt.Sscanf(e.Path, "M%f %f C%f %f, %f %f, %f %f", &p[0], &p[1], &p[2], &p[3], &p[4], &p[5], &p[6], &p[7])
		if err != nil || e.fixedLabel {
			taken = append(taken, labelBox(e.LabelX, e.LabelY, e.Label))

			continue
		}

		best, bestCover := box{}, -1

		for _, t := range labelTs {
			u := 1 - t
			at := func(a, b, c, d float64) int {
				return int(u*u*u*a + 3*u*u*t*b + 3*u*t*t*c + t*t*t*d)
			}
			x, y := at(p[0], p[2], p[4], p[6]), at(p[1], p[3], p[5], p[7])
			b := labelBox(x, y, e.Label)

			cover := 0
			for _, o := range taken {
				cover += b.overlap(o)
			}

			if bestCover < 0 || cover < bestCover {
				best, bestCover = b, cover
				e.LabelX, e.LabelY = x, y
			}

			if cover == 0 {
				break
			}
		}

		taken = append(taken, best)
	}
}

// sameColumnAppEdges reports whether an app calls another app of the same
// list (through an Envoy sidecar).
func sameColumnAppEdges(list []*appGroup) bool {
	names := map[string]bool{}
	for _, grp := range list {
		names[grp.name] = true
	}

	for _, grp := range list {
		for _, e := range grp.instances[0].Uses {
			if e.ToApp && names[e.To] {
				return true
			}
		}
	}

	return false
}

// appNode is an app's card at x, y: one card for all its instances, its
// first instance's sidecars as chips.
func appNode(grp *appGroup, x, y int, sidecars map[string][]row, selected string) node {
	n := node{row: grp.instances[0], X: x, Y: y, H: nodeH}
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

	return n
}

// flowEdge draws one edge in the flow layout: forward to a later column,
// round through the gutter between apps of the first column, out to the
// right and back within any other column, or against the flow.
func flowEdge(from, to node, e core.Edge, firstX int) edge {
	label, plain := edgeText(e), !e.TLS

	switch {
	case to.X > from.X:
		return curve(from.CX(), from.CY(), to, label, plain, e.Reason, false)
	case to.X == from.X && from.X == firstX && from.Kind != "infra":
		ed := loop(from, to, label)
		ed.Plaintext, ed.Reason, ed.fixedLabel = plain, e.Reason, true

		return ed
	case to.X == from.X:
		x1, y1 := from.CX(), from.CY()

		x2, y2 := to.CX()+6, to.CY()
		if to.Pod() {
			x2 += podPad
		}

		bend := max(x1, x2) + sideBend

		return edge{
			Path:   fmt.Sprintf("M%d %d C%d %d, %d %d, %d %d", x1, y1, bend, y1, bend, y2, x2, y2),
			LabelX: bend - 8, LabelY: (y1 + y2) / 2,
			Label: label, Plaintext: plain, Reason: e.Reason, ToApp: e.ToApp,
		}
	}
	// Against the flow: out of the left side, into the target's right.
	x1, y1 := from.X, from.CY()
	if from.Pod() {
		x1 -= podPad
	}

	x2, y2 := to.CX()+6, to.CY()
	if to.Pod() {
		x2 += podPad
	}

	mid := (x1 + x2) / 2

	return edge{
		Path:   fmt.Sprintf("M%d %d C%d %d, %d %d, %d %d", x1, y1, mid, y1, mid, y2, x2, y2),
		LabelX: mid, LabelY: (y1 + y2) / 2,
		Label: label, Plaintext: plain, Reason: e.Reason, ToApp: e.ToApp,
	}
}

// edgeText labels an edge: TLS :port or plaintext :port, and for a
// proxy's upstream its mode: TLS :port (re-encrypt), terminate :port
// (a plaintext hop on a private segment), passthrough :port.
func edgeText(e core.Edge) string {
	switch e.Mode {
	case "":
		return edgeLabel(e.TLS, e.Port)
	case "off":
		return fmt.Sprintf("terminate :%d", e.Port)
	case "passthrough":
		return fmt.Sprintf("passthrough :%d", e.Port)
	}

	return fmt.Sprintf("TLS :%d", e.Port)
}

// modeWords explains a proxy upstream's mode for the inspector.
func modeWords(e core.Edge) string {
	switch e.Mode {
	case "off":
		return "Terminate: the proxy decrypts and sends plaintext over a private segment only it and " + e.To + " share."
	case "passthrough":
		return "Passthrough: the client's TLS session goes to " + e.To + " untouched, routed by SNI; the client sees " + e.To + "'s own certificate."
	case "on":
		return "Re-encrypt: the proxy decrypts, then opens TLS to " + e.To + " and verifies its certificate."
	}

	return ""
}

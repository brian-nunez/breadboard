package web

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"

	"github.com/a-h/templ"

	"github.com/brian-nunez/breadboard/internal/core"
)

// The docs are authored as templ components, one file per technology
// (docs_<id>.templ), each registering itself in init. A technology page is
// an overview; its guides cover basic usage and features, each explained as
// what actually happens, with the real code (D-061).

// tech is one technology's docs.
type tech struct {
	ID      string // catalog type, or "getting-started"
	Name    string
	Tagline string // one line under the title and on the docs home card
	Order   int    // position on the docs home and in the sidebar
	// Intro is the "What is ..." prose.
	Intro templ.Component
	// GoodFit and PoorFit list what the technology is and is not for.
	GoodFit, PoorFit []string
	// Diagram is an inline SVG figure for the overview.
	Diagram templ.Component
	// Official links to the project's own documentation.
	Official []link
	// Guides are the pages under this technology, in reading order.
	Guides []guide
	// Starters are starter ids that use this technology.
	Starters []string
}

// guide is one page under a technology.
type guide struct {
	Slug    string
	Title   string
	Summary string
	// Sections are the page's h2 headings, for "On this page"; ids match the
	// docH2 calls in Body.
	Sections []docSection
	// Starter is the starter whose code the guide quotes, if any.
	Starter string
	// Live, when set, names a fragment path that shows this concept in the
	// reader's running experiment; the page loads it after rendering.
	Live string
	Body templ.Component
}

type docSection struct{ ID, Title string }

type link struct{ Title, URL string }

var techs []tech

func registerTech(t tech) {
	techs = append(techs, t)
	slices.SortStableFunc(techs, func(a, b tech) int { return cmp.Compare(a.Order, b.Order) })
}

func findTech(id string) (tech, bool) {
	i := slices.IndexFunc(techs, func(t tech) bool { return t.ID == id })
	if i < 0 {
		return tech{}, false
	}
	return techs[i], true
}

func (t tech) guide(slug string) (guide, bool) {
	i := slices.IndexFunc(t.Guides, func(g guide) bool { return g.Slug == slug })
	if i < 0 {
		return guide{}, false
	}
	return t.Guides[i], true
}

// docsData is what docs pages need beyond their own content.
type docsData struct {
	Version  string
	Catalog  map[string]core.CatalogEntry
	Starters map[string]core.StarterInfo
}

func (s *Server) docsData(r *http.Request) docsData {
	d := docsData{Version: s.eng.Version().Version, Catalog: map[string]core.CatalogEntry{}, Starters: map[string]core.StarterInfo{}}
	if c, err := s.eng.Catalog(r.Context()); err == nil {
		for _, e := range c.Entries {
			d.Catalog[e.Type] = e
		}
	}
	if st, err := s.eng.Starters(r.Context()); err == nil {
		for _, x := range st.Starters {
			d.Starters[x.ID] = x
		}
	}
	return d
}

func (s *Server) docsHome(w http.ResponseWriter, r *http.Request) {
	render(w, r, docsHomePage(s.docsData(r)))
}

func (s *Server) docsTech(w http.ResponseWriter, r *http.Request) {
	t, ok := findTech(r.PathValue("tech"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	render(w, r, techPage(s.docsData(r), t))
}

func (s *Server) docsGuide(w http.ResponseWriter, r *http.Request) {
	t, ok := findTech(r.PathValue("tech"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	g, ok := t.guide(r.PathValue("guide"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	render(w, r, guidePage(s.docsData(r), t, g))
}

// kafkaLive is the sentence the consumer-groups guide shows about the
// reader's own experiment.
type kafkaLive struct{ Sentence string }

// liveFragment renders a guide's "in your experiment right now" note, or
// nothing (an empty 200) when the experiment has nothing to show.
func (s *Server) liveFragment(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("name") {
	case "kafka-groups":
		d := s.dashboard(r.Context())
		for _, row := range d.Services {
			if row.Type != "kafka" || row.State != "running" {
				continue
			}
			k, err := s.eng.KafkaTopics(r.Context(), core.ExploreRequest{Dir: s.dir, Service: row.Name})
			if err != nil {
				return
			}
			if sentence := groupSentence(k); sentence != "" {
				render(w, r, kafkaGroupsLive(d.Experiment, row.Name, &kafkaLive{Sentence: sentence}))
			}
			return
		}
	default:
		http.NotFound(w, r)
	}
}

// groupSentence describes the first group of the first topic it consumes.
func groupSentence(k *core.KafkaOverview) string {
	if len(k.Groups) == 0 || len(k.Groups[0].Topics) == 0 {
		return ""
	}
	g := k.Groups[0]
	partitions := 0
	for _, t := range k.Topics {
		if t.Name == g.Topics[0] {
			partitions = t.Partitions
		}
	}
	var owns string
	switch {
	case g.Members == 0:
		owns = "it has no active members, so nothing is reading"
	case g.Members == 1:
		owns = "it has 1 member, which owns every partition"
	case g.Members < partitions:
		owns = fmt.Sprintf("its %d members split the partitions between them", g.Members)
	case g.Members == partitions:
		owns = fmt.Sprintf("each of its %d members owns one partition", g.Members)
	default:
		owns = fmt.Sprintf("it has %d members, so %d of them sit idle", g.Members, g.Members-partitions)
	}
	return fmt.Sprintf("%s has %d partitions and the group %s reads it: %s. Its lag is %s messages.", g.Topics[0], partitions, g.Name, owns, commas(g.Lag))
}

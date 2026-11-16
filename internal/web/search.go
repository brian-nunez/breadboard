package web

import (
	"encoding/json"
	"net/http"
)

// searchEntry is one page the search dialog can open.
type searchEntry struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
	URL   string `json:"url"`
}

// searchIndex lists every page: docs, starters, and the experiment's
// services. The browser filters it; nothing is searched on the server.
func (s *Server) searchIndex(w http.ResponseWriter, r *http.Request) {
	var out []searchEntry
	for _, t := range techs {
		out = append(out, searchEntry{t.Name, "Docs", "/docs/" + t.ID})
		for _, g := range t.Guides {
			out = append(out, searchEntry{g.Title, "Docs · " + t.Name, "/docs/" + t.ID + "/" + g.Slug})
		}
	}
	d := s.docsData(r)
	for _, id := range starterOrder(d) {
		out = append(out, searchEntry{cardTitle(d.Starters[id].Title), "Starter · " + id, "/starters/" + id})
	}
	for _, row := range s.dashboard(r.Context()).Services {
		out = append(out, searchEntry{row.Name, "Service", "/services/" + row.Name})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

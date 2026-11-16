package web

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"
)

// stream sends a server-sent event named "update" carrying freshly rendered
// HTML every refresh interval, but only when the HTML changed. The page's
// script swaps it into the element that opened the stream.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, build func(context.Context) templComponent) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	var last string
	tick := time.NewTicker(s.refresh)
	defer tick.Stop()
	for {
		var buf bytes.Buffer
		if err := build(ctx).Render(ctx, &buf); err != nil {
			return
		}
		if html := buf.String(); html != last {
			last = html
			var ev strings.Builder
			ev.WriteString("event: update\n")
			for _, line := range strings.Split(html, "\n") {
				ev.WriteString("data: ")
				ev.WriteString(line)
				ev.WriteString("\n")
			}
			ev.WriteString("\n")
			if _, err := w.Write([]byte(ev.String())); err != nil {
				return
			}
			flusher.Flush()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

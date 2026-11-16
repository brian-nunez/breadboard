package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Every registered technology and guide renders, its "On this page" links
// point at headings that exist, and its official links are external.
func TestEveryDocsPageRenders(t *testing.T) {
	s := newServer(t)
	cookie := map[string]string{"Cookie": signIn(t, s)}
	home := s.get(t, "/docs", cookie).Body.String()
	if len(techs) == 0 {
		t.Fatal("no technologies registered")
	}
	for _, tc := range techs {
		if !strings.Contains(home, `href="/docs/`+tc.ID+`"`) {
			t.Errorf("docs home does not link %s", tc.ID)
		}
		rec := s.get(t, "/docs/"+tc.ID, cookie)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "What is "+tc.Name) {
			t.Errorf("/docs/%s = %d", tc.ID, rec.Code)
		}
		for _, l := range tc.Official {
			if !strings.HasPrefix(l.URL, "https://") {
				t.Errorf("%s official link %q is not https", tc.ID, l.URL)
			}
		}
		if len(tc.Guides) == 0 {
			t.Errorf("%s has no guides", tc.ID)
		}
		for _, g := range tc.Guides {
			path := "/docs/" + tc.ID + "/" + g.Slug
			rec := s.get(t, path, cookie)
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, ">"+g.Title+"</h1>") {
				t.Errorf("%s = %d", path, rec.Code)
				continue
			}
			for _, sec := range g.Sections {
				if !strings.Contains(body, `id="`+sec.ID+`"`) {
					t.Errorf("%s: On this page links #%s, but no heading has that id", path, sec.ID)
				}
			}
		}
	}
	for _, path := range []string{"/docs/nope", "/docs/kafka/nope"} {
		if rec := s.get(t, path, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d", path, rec.Code)
		}
	}
}

func TestGroupSentence(t *testing.T) {
	for _, c := range []struct {
		members int
		want    string
	}{
		{0, "no active members"},
		{1, "1 member, which owns every partition"},
		{2, "its 2 members split the partitions"},
		{3, "each of its 3 members owns one partition"},
		{5, "5 members, so 2 of them sit idle"},
	} {
		k := kafkaOverviewFor(c.members)
		if got := groupSentence(k); !strings.Contains(got, c.want) || !strings.Contains(got, "orders has 3 partitions") {
			t.Errorf("members=%d: %q", c.members, got)
		}
	}
}

func kafkaOverviewFor(members int) *core.KafkaOverview {
	return &core.KafkaOverview{
		Topics: []core.KafkaTopic{{Name: "orders", Partitions: 3}},
		Groups: []core.KafkaGroup{{Name: "orders-consumers", Topics: []string{"orders"}, Members: members, Lag: 1204}},
	}
}

package server

import (
	"net/url"
	"testing"
)

func TestScrapePreviewURLPreservesOpaqueIdentity(t *testing.T) {
	position := 0.4
	for _, id := range []string{"abc+123", "abc&pos=1", "id#fragment", "id%2B/中文:01"} {
		for _, still := range []bool{false, true} {
			target := scrapePreviewURL("primary", "Source+&X", id, &position)
			if still {
				target = scrapeStillURL("Source+&X", id, 2)
			}
			u, err := url.Parse(target)
			if err != nil {
				t.Fatal(err)
			}
			q, err := url.ParseQuery(u.RawQuery)
			if err != nil {
				t.Fatal(err)
			}
			if u.Fragment != "" || q.Get("provider") != "Source+&X" || q.Get("id") != id {
				t.Fatalf("identity changed: %s => %v", target, q)
			}
			if still {
				if len(q) != 4 || q.Get("kind") != "still" || q.Get("index") != "2" || q.Has("pos") {
					t.Fatalf("still params=%v", q)
				}
			} else if len(q) != 4 || q.Get("kind") != "primary" || q.Get("pos") != "0.4" {
				t.Fatalf("primary params=%v", q)
			}
		}
	}
}

package nfo

import (
	"strings"
	"testing"
)

func TestUpdateMetaTubeIdentityCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		overwrite  bool
		want       string
	}{
		{"legacy only missing", `<metatubeid>Old:original</metatubeid>`, false, "Old:original"},
		{"mixed unique only missing", `<uniqueid type="MeTaTuBe">Old:original</uniqueid>`, false, "Old:original"},
		{"empty marker filled", `<uniqueid type="metatube"></uniqueid>`, false, "New:a%2Bb"},
		{"legacy overwrite", `<metatubeid>Old:original</metatubeid>`, true, "New:a%2Bb"},
		{"both overwrite", `<uniqueid type="metatube">Old:original</uniqueid><metatubeid>Older:stale</metatubeid>`, true, "New:a%2Bb"},
		{"provider lock", `<lockedfields>ProviderIds</lockedfields><metatubeid>Old:original</metatubeid>`, true, "Old:original"},
		{"all locked", `<lockdata>true</lockdata><metatubeid>Old:original</metatubeid>`, true, "Old:original"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<movie>` + tc.body + `<uniqueid type="tmdb">123</uniqueid><actor><name>A</name><metatubeid>Actor:unchanged</metatubeid></actor></movie>`
			path := writeTempNFO(t, body)
			if err := UpdateScraped(path, ScrapeFields{ProviderID: "New:a%2Bb", Overwrite: tc.overwrite}); err != nil {
				t.Fatal(err)
			}
			raw := readTempNFO(t, path)
			if got := existingMetaTubeID(raw); got != tc.want {
				t.Fatalf("identity %q want %q: %s", got, tc.want, raw)
			}
			if tc.want == "New:a%2Bb" && strings.Contains(tc.body, "<metatubeid>") && ScrapedTagValue(raw, "metatubeid") != tc.want {
				t.Fatalf("stale legacy tag: %s", raw)
			}
			if !strings.Contains(raw, `<uniqueid type="tmdb">123</uniqueid>`) || !strings.Contains(raw, `<metatubeid>Actor:unchanged</metatubeid>`) {
				t.Fatalf("unrelated metadata changed: %s", raw)
			}
			if tc.want == "Old:original" && raw != body {
				t.Fatalf("protected identity was rewritten: %s", raw)
			}
		})
	}
}

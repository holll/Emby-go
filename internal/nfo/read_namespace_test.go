package nfo

import "testing"

func TestReadSeparatesTopLevelNamespaceExtensions(t *testing.T) {
	for _, fixture := range []struct {
		name, xml, title, plot string
		year, genres           int
	}{
		{"standard fields", `<movie xmlns:v="urn:vendor"><title>Title</title><plot>Plot</plot><year>2020</year><genre>Genre</genre><v:title>Other</v:title><v:plot>Other</v:plot><v:year>2099</v:year><v:genre>Other</v:genre></movie>`, "Title", "Plot", 2020, 1},
		{"extensions only", `<movie xmlns:v="urn:vendor"><v:title>Other</v:title><v:plot>Other</v:plot><v:year>not-a-number</v:year><v:genre>Other</v:genre></movie>`, "", "", 0, 0},
		{"legacy namespaced root", `<movie xmlns="urn:legacy"><title>Legacy</title><plot>Legacy plot</plot><year>2020</year><genre>Genre</genre></movie>`, "Legacy", "Legacy plot", 2020, 1},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := writeTempNFO(t, fixture.xml)
			meta, err := Read(path)
			if err != nil || meta.Title != fixture.title || meta.Plot != fixture.plot || meta.Year != fixture.year || len(meta.Genres) != fixture.genres {
				t.Fatalf("metadata=%+v err=%v", meta, err)
			}
			if got := readTempNFO(t, path); got != fixture.xml {
				t.Fatalf("read modified file: %s", got)
			}
		})
	}
}

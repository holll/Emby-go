package server

import (
	"path/filepath"
	"strconv"
	"testing"

	"emby-go/internal/scanner"
	"emby-go/internal/store"
)

func TestEditNFOPathForStackedMovie(t *testing.T) {
	source := filepath.Join("media", "GOPJ-559-CD1.strm")
	for _, tc := range []struct {
		name  string
		movie store.Movie
		want  string
	}{
		{"new stack", store.Movie{SourcePath: source, AdditionalParts: []string{"part2.strm"}}, filepath.Join("media", "GOPJ-559.nfo")},
		{"standalone", store.Movie{SourcePath: source}, filepath.Join("media", "GOPJ-559-CD1.nfo")},
		{"legacy", store.Movie{SourcePath: source, NFOPath: "legacy.nfo", AdditionalParts: []string{"part2.strm"}}, "legacy.nfo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := editNFOPath(tc.movie); got != tc.want {
				t.Fatalf("path=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestManualEditCreatesSharedStackedNFO(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(strconv.FormatBool(existing), func(t *testing.T) {
			app, media := detailTestApp(t, t.TempDir(), `<movie><title>Other</title></movie>`)
			ts, token := detailServer(t, app)
			lib, err := app.db.AddLibrary("L", media)
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(media, "GOPJ-559-CD1.strm")
			part := filepath.Join(media, "GOPJ-559-CD2.strm")
			for _, file := range []string{source, part} {
				writeFile(t, file, "https://media.test/video.mp4\n")
			}
			wantPath := filepath.Join(media, "GOPJ-559.nfo")
			if existing {
				// Before upgrading, a valid legacy sidecar wins over a shared sidecar.
				// Edits must update and reindex that same registered file.
				writeFile(t, wantPath, `<movie><title>Shared unchanged</title></movie>`)
				wantPath = filepath.Join(media, "GOPJ-559-CD1.nfo")
				writeFile(t, wantPath, `<movie><title>Legacy</title></movie>`)
			}
			if _, err := scanner.Scan(app.db, lib); err != nil {
				t.Fatal(err)
			}
			id, err := app.db.MovieIDByPath(source)
			if err != nil {
				t.Fatal(err)
			}
			resp, body := embyCall(t, ts, "PUT", "/api/admin/items/"+strconv.FormatInt(id, 10), token, `{"title":"Shared manual title"}`)
			if resp.StatusCode != 200 || body["status"] != "success" || body["indexed"] != true {
				t.Fatalf("edit=%d %+v", resp.StatusCode, body)
			}
			movie, err := app.db.Movie(id)
			if err != nil {
				t.Fatal(err)
			}
			if movie.NFOPath != wantPath || movie.Title != "Shared manual title" || movie.Status != "manual" {
				t.Fatalf("reindexed movie=%+v", movie)
			}
		})
	}
}

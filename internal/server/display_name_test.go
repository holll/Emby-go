package server

import (
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	"emby-go/internal/store"
)

func TestMovieDisplayDTO(t *testing.T) {
	for _, tc := range []struct {
		name  string
		movie store.Movie
		title string
		sort  string
	}{
		{"title and sort suffix", store.Movie{Title: "影片-CD1", SortName: "排序_cd2"}, "影片", "排序"},
		{"number fallback", store.Movie{Number: "ABC-123_CD2"}, "ABC-123", "ABC-123"},
		{"filename fallback", store.Movie{SourcePath: "/library/ABC-123-CD1.strm"}, "ABC-123", "ABC-123"},
		{"internal CD text", store.Movie{Title: "影片-CD1 特别版", SortName: "ACDC"}, "影片-CD1 特别版", "ACDC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.movie
			m.ID, m.LibraryID = 42, 7
			m.AdditionalParts = []string{"/library/ABC-123-CD2.strm"}
			before := m
			a := &App{serverID: "display-test"}
			item := a.embyItemActors(m, store.UserData{}, nil, true)
			if item["Name"] != tc.title || item["SortName"] != tc.sort || item["Id"] != "42" || item["PartCount"] != 2 || item["ParentId"] != externalLibraryID(7) {
				t.Fatalf("movie DTO: %+v", item)
			}
			part := a.partItem(m, 0)
			if part["Name"] != tc.title+" - 第 2 段" || part["SortName"] != tc.sort || part["Id"] != "part-42-2" || part["ParentId"] != "42" || part["PartCount"] != 2 {
				t.Fatalf("part DTO: %+v", part)
			}
			if !reflect.DeepEqual(m, before) {
				t.Fatalf("display mapping mutated raw movie: %+v", m)
			}
		})
	}
}

func TestMediaSourceDisplayName(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name, url, want string
	}{
		{"hyphen", "https://media.test/ABC-123-CD1.mp4?token=keep-CD2", "ABC-123"},
		{"underscore", "https://media.test/ABC-123_cd2.mkv", "ABC-123"},
		{"space", "https://media.test/ABC-123%20CD3.mp4", "ABC-123"},
		{"internal", "https://media.test/ABC-CD1-trailer.mp4", "ABC-CD1-trailer"},
		{"zero", "https://media.test/ABC-CD0.mp4", "ABC-CD0"},
		{"no separator", "https://media.test/ACDC1.mp4", "ACDC1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(root, tc.name+"-CD1.strm")
			writeFile(t, source, tc.url+"\n")
			m := store.Movie{Title: "影片-CD1", SourcePath: source}
			for _, path := range []string{"", source} {
				if got := mediaSourceName(m, path); got != tc.want {
					t.Errorf("mediaSourceName(%q)=%q, want %q", path, got, tc.want)
				}
			}
		})
	}
	for _, m := range []store.Movie{
		{Title: "影片-CD1"},
		{Number: "影片-CD1"},
		{SourcePath: filepath.Join(root, "影片-CD1.strm")},
	} {
		if got := mediaSourceName(m, ""); got != "影片" {
			t.Errorf("fallback mediaSourceName(%+v)=%q", m, got)
		}
	}
}

func TestMovieWebhookDisplayName(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	a := &App{}
	movie := store.Movie{ID: 42, LibraryID: 7, Title: "影片-CD1", SourcePath: "/library/影片-CD1.strm"}
	event := a.movieWebhook(c, "item.updated", movie)
	if event.Item.Name != "影片" || event.Item.Id != "42" || event.Item.LibraryId != "7" || event.Item.Path != movie.SourcePath {
		t.Fatalf("webhook display or identity changed: %+v", event.Item)
	}
	movie.Title = ""
	if event := a.movieWebhook(c, "item.updated", movie); event.Item.Name != "" {
		t.Fatalf("empty webhook title must not gain a filename fallback: %+v", event.Item)
	}
}

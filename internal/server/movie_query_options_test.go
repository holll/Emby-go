package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

func TestParseMovieQueryOptions(t *testing.T) {
	defaults := movieQueryOptions{sortBy: "SortName"}
	for _, tc := range []struct {
		name  string
		query url.Values
		want  movieQueryOptions
	}{
		{"defaults", nil, defaults},
		{"empty sort", url.Values{"SortBy": {""}, "SortOrder": {""}}, defaults},
		{"blank sort keys", url.Values{"SortBy": {" , , "}}, defaults},
		{"first nonempty sort key", url.Values{"SortBy": {" , DateCreated , SortName"}, "SortOrder": {"dEsCeNdInG"}}, movieQueryOptions{sortBy: "DateCreated", desc: true}},
		{"sort order is not trimmed", url.Values{"SortOrder": {" Descending "}}, defaults},
		{"sort key retains case", url.Values{"SortBy": {" year , SortName"}}, movieQueryOptions{sortBy: "year"}},
		{"entity names", url.Values{"Genres": {" Drama, , Comedy "}, "Tags": {" Tag One "}, "Studios": {" Studio One "}, "Person": {" Actor One "}}, movieQueryOptions{sortBy: "SortName", genre: "Drama,Comedy", tags: "Tag One", studios: "Studio One", person: "Actor One"}},
		{"entity ID aliases", url.Values{"GenreIds": {entityId("Genre", "剧情")}, "TagIds": {entityId("Tag", "Tag,One")}, "StudioIds": {entityId("Studio", "Studio One")}, "PersonIds": {entityId("Person", "Actor One")}}, movieQueryOptions{sortBy: "SortName", genre: "剧情", tags: "Tag,One", studios: "Studio One", person: "Actor One"}},
		{"names before IDs without deduplication", url.Values{"Genres": {"Drama"}, "GenreIds": {entityId("Genre", "Drama") + "," + entityId("Genre", "Comedy")}}, movieQueryOptions{sortBy: "SortName", genre: "Drama,Drama,Comedy"}},
		{"invalid and wrong-kind IDs ignored", url.Values{"GenreIds": {"invalid," + entityId("Tag", "Wrong")}, "TagIds": {entityId("Genre", "Wrong")}}, defaults},
		{"mixed-case filters", url.Values{"Filters": {"IsUnPlAyEd,iSfAvOrItE"}}, movieQueryOptions{sortBy: "SortName", unplayed: true, favorite: true}},
		{"filter substring behavior", url.Values{"Filters": {"NotIsUnplayedSuffix,NotIsFavoriteSuffix"}}, movieQueryOptions{sortBy: "SortName", unplayed: true, favorite: true}},
		{"unrelated filters", url.Values{"Filters": {"IsPlayed,IsFavoriteOrLiked"}}, movieQueryOptions{sortBy: "SortName", favorite: true}},
		{"term and years remain raw", url.Values{"SearchTerm": {"  A&B + 中文  "}, "Years": {" 2020,invalid,2022 "}}, movieQueryOptions{sortBy: "SortName", term: "  A&B + 中文  ", years: " 2020,invalid,2022 "}},
		{"first query value wins", url.Values{"SearchTerm": {"first", "second"}, "Years": {"2020", "2021"}, "Genres": {"Drama", "Comedy"}}, movieQueryOptions{sortBy: "SortName", term: "first", years: "2020", genre: "Drama"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/Items?"+tc.query.Encode(), nil)
			if got := parseMovieQueryOptions(c); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMovieQueryOptionsBothScopes(t *testing.T) {
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.db.AddLibrary("Other query scope", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"A Match", "B Match", "C Outside", "D Match"} {
		lib, collection := libs[0], "Query Set"
		if i == 2 {
			collection = "Other Set"
		}
		if i == 3 {
			lib = other
		}
		id, err := a.db.IndexMovie(store.Movie{
			LibraryID: lib.ID, SourcePath: filepath.Join(lib.Path, fmt.Sprintf("query-%d.strm", i)),
			Title: name, SortName: name, Collection: collection, Year: 2020 + i,
			Genres: []string{"Drama"}, Tags: []string{"Query Tag"}, Studios: []string{"Query Studio"}, Status: "success",
		}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 || i == 3 {
			if err := a.db.SetIndexedFavorite(id, true); err != nil {
				t.Fatal(err)
			}
		}
		if i == 1 {
			if err := a.db.SetIndexedPlayed(id, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, scope := range []struct {
		name, parent                              string
		baseline, descending, favorites, unplayed []string
	}{
		{"library", externalLibraryID(libs[0].ID), []string{"A Match", "B Match", "C Outside"}, []string{"C Outside", "B Match", "A Match"}, []string{"A Match"}, []string{"A Match", "C Outside"}},
		{"collection", boxsetID("Query Set"), []string{"A Match", "B Match", "D Match"}, []string{"D Match", "B Match", "A Match"}, []string{"A Match", "D Match"}, []string{"A Match", "D Match"}},
	} {
		for _, tc := range []struct {
			name  string
			query url.Values
			want  []string
		}{
			{"default sort", nil, scope.baseline},
			{"first sort key", url.Values{"SortBy": {" , ProductionYear,SortName"}, "SortOrder": {"dEsCeNdInG"}}, scope.descending},
			{"names and ID aliases", url.Values{"Genres": {"Drama"}, "TagIds": {entityId("Tag", "Query Tag")}, "StudioIds": {entityId("Studio", "Query Studio")}}, scope.baseline},
			{"favorite", url.Values{"Filters": {"iSfAvOrItE"}}, scope.favorites},
			{"unplayed", url.Values{"Filters": {"iSuNpLaYeD"}}, scope.unplayed},
			{"combined filters", url.Values{"Filters": {"iSuNpLaYeD,iSfAvOrItE"}}, scope.favorites},
			{"term and years", url.Values{"SearchTerm": {"Match"}, "Years": {"2020,2023"}}, scope.favorites},
			{"term excludes all", url.Values{"SearchTerm": {"missing title"}}, []string{}},
		} {
			t.Run(scope.name+"/"+tc.name, func(t *testing.T) {
				q := url.Values{"ParentId": {scope.parent}, "IncludeItemTypes": {"Movie"}}
				for k, v := range tc.query {
					q[k] = v
				}
				// Repeat ordinary queries to exercise cache hits as well as initial reads.
				for repeat := 0; repeat < 2; repeat++ {
					status, raw := embyDo(t, ts, http.MethodGet, "/emby/Items?"+q.Encode(), token, "")
					var result struct {
						Items []struct{ Name string }
						Total int `json:"TotalRecordCount"`
					}
					if status != http.StatusOK || json.Unmarshal(raw, &result) != nil {
						t.Fatalf("status=%d body=%s", status, raw)
					}
					got := make([]string, 0, len(result.Items))
					for _, item := range result.Items {
						got = append(got, item.Name)
					}
					if !reflect.DeepEqual(got, tc.want) || result.Total != len(tc.want) {
						t.Fatalf("repeat %d: got %v total=%d, want %v", repeat, got, result.Total, tc.want)
					}
				}
			})
		}
	}
}

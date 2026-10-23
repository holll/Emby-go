package store

import (
	"reflect"
	"testing"
)

func TestEntityVisibleScopesNamesAndArt(t *testing.T) {
	s, public := newFeatureStore(t)
	private, err := s.AddLibrary("private", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range []Movie{
		{LibraryID: public, SourcePath: "/entities/public", Status: "success", Collection: "Shared", Genres: []string{"Public", "Common"}, Tags: []string{"Public", "Common"}, Studios: []string{"Public", "Common"}, PosterPath: "public.jpg"},
		{LibraryID: private.ID, SourcePath: "/entities/private", Status: "success", Collection: "Shared", Genres: []string{"Secret", "Common"}, Tags: []string{"Secret", "Common"}, Studios: []string{"Secret", "Common"}, PosterPath: "private.jpg"},
		{LibraryID: public, SourcePath: "/entities/pending", Status: "pending", Collection: "Shared", Genres: []string{"Pending"}, Tags: []string{"Pending"}, Studios: []string{"Pending"}, PosterPath: "pending.jpg"},
	} {
		id := upsertFixtureMovie(t, s, m.LibraryID, m)
		name := "Public"
		if i == 1 {
			name = "Secret"
		}
		if i == 2 {
			name = "Pending"
		}
		if err := s.ReplaceActors(id, []ActorRef{{Name: name}, {Name: "Common"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"Genre", "Tag", "Studio", "Person"} {
		t.Run(kind, func(t *testing.T) {
			got, err := s.EntityNamesVisible(kind, 0, []int64{public}, "Shared")
			if err != nil || !reflect.DeepEqual(got, []string{"Common", "Public"}) {
				t.Fatalf("names=%v err=%v", got, err)
			}
			art, err := s.EntityPosterVisible(kind, "Common", 0, []int64{public}, "Shared")
			if err != nil || art != "public.jpg" {
				t.Fatalf("art=%q err=%v", art, err)
			}
			for _, scope := range []struct {
				library    int64
				visible    []int64
				collection string
			}{{0, []int64{}, ""}, {private.ID, []int64{public}, ""}, {0, []int64{public}, "Missing"}} {
				got, err = s.EntityNamesVisible(kind, scope.library, scope.visible, scope.collection)
				if err != nil || len(got) != 0 {
					t.Fatalf("empty scope names=%v err=%v", got, err)
				}
				art, err = s.EntityPosterVisible(kind, "Common", scope.library, scope.visible, scope.collection)
				if err != nil || art != "" {
					t.Fatalf("empty scope art=%q err=%v", art, err)
				}
			}
			got, err = s.EntityNamesVisible(kind, 0, nil, "*")
			if err != nil || !reflect.DeepEqual(got, []string{"Common", "Public", "Secret"}) {
				t.Fatalf("admin names=%v err=%v", got, err)
			}
		})
	}
}

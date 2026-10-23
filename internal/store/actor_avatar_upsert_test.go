package store

import (
	"reflect"
	"testing"
	"time"
)

func TestActorAvatarSettersSharedUpsertSemantics(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		name := "legacy"
		if indexed {
			name = "indexed"
		}
		t.Run(name, func(t *testing.T) {
			f := actorIndexFixture(t)
			set := f.s.SetActorAvatar
			if indexed {
				set = f.s.SetIndexedActorAvatar
			}
			checkVersions := func(before map[string]string, changed bool) {
				t.Helper()
				if indexed && changed {
					assertActorIndexBumps(t, f, before, f.lib, f.other)
				} else if !reflect.DeepEqual(actorIndexVersions(t, f), before) || f.s.Version("g:version") != before["g:version"] {
					t.Fatal("unexpected version change")
				}
			}
			const stamp = "2001-02-03T04:05:06Z"
			if _, err := f.s.db.Exec("UPDATE actors SET avatar_url='old-url',avatar_tag='old-tag',updated_at=? WHERE name=?", stamp, indexedAvatarActor); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ url, tag, wantURL, wantTag string }{
				{"", "", "old-url", "old-tag"},
				{"new-url", "", "new-url", "old-tag"},
				{"", "new-tag", "new-url", "new-tag"},
				{"final-url", "final-tag", "final-url", "final-tag"},
			} {
				before := actorIndexVersions(t, f)
				if err := set(" \t"+indexedAvatarActor+"\n", tc.url, tc.tag); err != nil {
					t.Fatal(err)
				}
				actor, err := f.s.ActorAvatar(indexedAvatarActor)
				if err != nil || actor.AvatarURL != tc.wantURL || actor.AvatarTag != tc.wantTag {
					t.Fatalf("actor = %+v, err = %v", actor, err)
				}
				var gotStamp string
				if err := f.s.db.QueryRow("SELECT updated_at FROM actors WHERE name=?", indexedAvatarActor).Scan(&gotStamp); err != nil || gotStamp != stamp {
					t.Fatalf("conflict timestamp = %q, err = %v", gotStamp, err)
				}
				checkVersions(before, true)
			}
			before := actorIndexVersions(t, f)
			snapshot := indexSnapshot(t, f.s)
			if err := set(" \t\n", "ignored", "ignored"); err != nil || indexSnapshot(t, f.s) != snapshot {
				t.Fatalf("blank name changed state: %v", err)
			}
			checkVersions(before, false)

			// Both paths insert empty fields and a UTC RFC3339 timestamp verbatim.
			before = actorIndexVersions(t, f)
			start := time.Now().UTC().Truncate(time.Second)
			if err := set(" New actor ", "", ""); err != nil {
				t.Fatal(err)
			}
			var url, tag, insertedAt string
			if err := f.s.db.QueryRow("SELECT avatar_url,avatar_tag,updated_at FROM actors WHERE name='New actor'").Scan(&url, &tag, &insertedAt); err != nil {
				t.Fatal(err)
			}
			parsed, err := time.Parse(time.RFC3339, insertedAt)
			if err != nil || url != "" || tag != "" || insertedAt != parsed.UTC().Format(time.RFC3339) || parsed.Before(start) || parsed.After(time.Now().UTC()) {
				t.Fatalf("insert = %q/%q at %q, err = %v", url, tag, insertedAt, err)
			}
			if indexed {
				assertActorIndexBumps(t, f, before)
			} else {
				checkVersions(before, false)
			}
		})
	}
}

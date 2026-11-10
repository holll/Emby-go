package store

import (
	"reflect"
	"testing"
	"time"
)

func TestIndexedActorAvatarUpsertSemantics(t *testing.T) {
	f := actorIndexFixture(t)
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
		if err := f.s.SetIndexedActorAvatar(" \t"+indexedAvatarActor+"\n", tc.url, tc.tag); err != nil {
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
		assertActorIndexBumps(t, f, before, f.lib, f.other)
	}
	before := actorIndexVersions(t, f)
	snapshot := indexSnapshot(t, f.s)
	if err := f.s.SetIndexedActorAvatar(" \t\n", "ignored", "ignored"); err != nil || indexSnapshot(t, f.s) != snapshot {
		t.Fatalf("blank name changed state: %v", err)
	}
	if !reflect.DeepEqual(actorIndexVersions(t, f), before) || f.s.Version("g:version") != before["g:version"] {
		t.Fatal("blank name changed versions")
	}

	// Insertion retains empty fields and publishes a UTC RFC3339 timestamp.
	before = actorIndexVersions(t, f)
	start := time.Now().UTC().Truncate(time.Second)
	if err := f.s.SetIndexedActorAvatar(" New actor ", "", ""); err != nil {
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
	assertActorIndexBumps(t, f, before)
}

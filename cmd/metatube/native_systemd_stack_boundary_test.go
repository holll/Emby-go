//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"emby-go/internal/scanner"
	"emby-go/internal/store"

	"golang.org/x/sys/unix"
)

// The root test owns fixture setup; only the running UID65534 service scans it.
func assertNativeSystemdStackBoundaries(t *testing.T, base, state, token string) {
	t.Helper()
	for _, mode := range []string{"full", "incremental"} {
		for _, dependency := range []string{"cd2-strm", "shared-nfo", "shared-with-sidecar", "sidecar-with-shared"} {
			t.Run("stack-boundary/"+mode+"/"+dependency, func(t *testing.T) {
				assertNativeSystemdStackBoundary(t, base, state, token, mode, dependency)
			})
		}
	}
}

func assertNativeSystemdStackBoundary(t *testing.T, base, state, token, mode, dependency string) {
	t.Helper()
	var fs unix.Statfs_t
	if err := unix.Statfs(state, &fs); err != nil || fs.Type != unix.EXT4_SUPER_MAGIC {
		t.Fatalf("stack fixture requires local ext4: type=%x err=%v", fs.Type, err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	call := func(method, path, body string) []byte {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Emby-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status=%d err=%v body=%s", method, path, resp.StatusCode, err, raw)
		}
		return raw
	}
	db, err := store.Open(filepath.Join(state, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	users, err := db.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	var userID int64
	for _, user := range users {
		if user.Name == "admin" {
			userID = user.ID
		}
	}
	if userID == 0 {
		t.Fatal("authenticated admin missing from database")
	}
	media := filepath.Join(state, "stack-boundary-"+mode+"-"+dependency)
	if err := os.Mkdir(media, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(media, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(media, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	const metadata = "<movie><title>Stack original</title><plot>Retained plot</plot><actor><name>Stack actor</name><role>Original role</role></actor></movie>"
	for _, name := range []string{"Film-CD1.strm", "Film-CD2.strm", "Z.strm"} {
		write(name, "https://example.invalid/"+name+".mp4\n")
	}
	write("Film.nfo", metadata)
	hasSidecar := dependency == "shared-with-sidecar" || dependency == "sidecar-with-shared"
	if hasSidecar {
		write("Film-CD1.nfo", metadata)
	}
	body, err := json.Marshal(map[string]string{"Name": mode + "-" + dependency, "Path": media})
	if err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, "/api/admin/libraries", string(body))
	libraries, err := db.Libraries()
	if err != nil {
		t.Fatal(err)
	}
	var libraryID int64
	for _, library := range libraries {
		if library.Path == media {
			libraryID = library.ID
		}
	}
	if libraryID == 0 {
		t.Fatal("created stack library missing")
	}
	scan := func(scanMode string) scanner.Result {
		t.Helper()
		raw := call(http.MethodPost, fmt.Sprintf("/api/admin/scan?library_id=%d&mode=%s", libraryID, scanMode), "")
		var result scanner.Result
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("scan result: %v %s", err, raw)
		}
		return result
	}
	if result := scan("full"); result.Success != 1 || result.Pending != 1 || result.Incomplete || !result.CleanupPerformed {
		t.Fatalf("baseline scan: %+v", result)
	}
	id, err := db.MovieIDByPath(filepath.Join(media, "Film-CD1.strm"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Movie(id)
	if err != nil || before.Title != "Stack original" || !reflect.DeepEqual(before.AdditionalParts, []string{filepath.Join(media, "Film-CD2.strm")}) {
		t.Fatalf("baseline stack: %+v err=%v", before, err)
	}
	actors, err := db.Actors(id)
	if err != nil || len(actors) != 1 || actors[0].Name != "Stack actor" {
		t.Fatalf("baseline actors: %+v err=%v", actors, err)
	}
	groups, err := db.LoadScanGroups(libraryID)
	if err != nil || len(groups) != 2 {
		t.Fatalf("baseline groups: %+v err=%v", groups, err)
	}
	// Seed genuine per-user state through existing store APIs, not test SQL.
	for i := range 3 {
		if _, err := db.RecordPlaybackEvent(store.PlaybackEvent{UserID: userID, MovieID: id, SessionKey: fmt.Sprintf("stack-fixture-%d", i), Kind: store.PlaybackStopped, PositionTicks: 120000000, Now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SetIndexedFavoriteDataForUser(userID, id, true); err != nil {
		t.Fatal(err)
	}
	personal, err := db.DataUser(userID, id)
	if err != nil || !personal.IsFavorite || personal.PositionTicks != 120000000 || personal.PlayCount != 3 {
		t.Fatalf("nonempty personal baseline: %+v err=%v", personal, err)
	}
	orphan := filepath.Join(media, "Z.strm")
	// Mkdir above must have succeeded; never remove anything outside this
	// fixed private fixture, even when the parent supplied a relative state.
	if filepath.Dir(media) != filepath.Clean(state) || filepath.Base(media) != "stack-boundary-"+mode+"-"+dependency || filepath.Dir(orphan) != media {
		t.Fatal("unexpected private fixture deletion path")
	}
	if err := os.Remove(orphan); err != nil {
		t.Fatal(err)
	}
	write("Healthy.strm", "https://example.invalid/healthy.mp4\n")
	write("Healthy.nfo", "<movie><title>Healthy stack boundary</title></movie>")
	target, limit := "Film.nfo", 8<<20
	if dependency == "cd2-strm" {
		target, limit = "Film-CD2.strm", 1<<20
	} else if dependency == "sidecar-with-shared" {
		target = "Film-CD1.nfo"
	}
	write(target, strings.Repeat("x", limit+1))
	result := scan(mode)
	if result.Skipped != 2 || result.Success != 1 || result.Pending != 0 || result.Failed != 0 || result.UnreadableNFO != 0 || !result.Incomplete || result.CleanupPerformed || result.Deleted != 0 {
		t.Fatalf("stack dependency bypassed bound/cleanup protection: %+v", result)
	}
	after, err := db.Movie(id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("fault changed complete Movie: before=%+v after=%+v err=%v", before, after, err)
	}
	afterActors, err := db.Actors(id)
	if err != nil || !reflect.DeepEqual(actors, afterActors) {
		t.Fatalf("fault changed actors: %+v err=%v", afterActors, err)
	}
	afterGroups, err := db.LoadScanGroups(libraryID)
	if err != nil {
		t.Fatal(err)
	}
	for key, group := range groups {
		if !reflect.DeepEqual(group, afterGroups[key]) {
			t.Fatalf("fault changed old group %s: before=%+v after=%+v", key, group, afterGroups[key])
		}
	}
	afterPersonal, err := db.DataUser(userID, id)
	if err != nil || !reflect.DeepEqual(personal, afterPersonal) {
		t.Fatalf("fault changed personal state: %+v err=%v", afterPersonal, err)
	}
	if _, err := db.MovieIDByPath(orphan); err != nil {
		t.Fatalf("incomplete scan deleted disk orphan from DB: %v", err)
	}
	// Verify the independent group committed before repairing the bad group.
	healthyID, err := db.MovieIDByPath(filepath.Join(media, "Healthy.strm"))
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := db.Movie(healthyID)
	if err != nil || healthy.Title != "Healthy stack boundary" {
		t.Fatalf("healthy group not immediately committed: %+v err=%v", healthy, err)
	}
	write("Film-CD2.strm", "https://example.invalid/recovered.mp4\n")
	write("Film.nfo", strings.Replace(metadata, "Stack original", "Stack recovered", 1))
	if hasSidecar {
		write("Film-CD1.nfo", strings.Replace(metadata, "Stack original", "Stack recovered", 1))
	}
	result = scan(mode)
	wantSuccess, wantUnchanged := 2, 0
	if mode == "incremental" {
		wantSuccess, wantUnchanged = 1, 1
	}
	if result.Success != wantSuccess || result.UnchangedGroups != wantUnchanged || result.Skipped != 0 || result.Failed != 0 || result.Pending != 0 || result.UnreadableNFO != 0 || result.Incomplete || !result.CleanupPerformed || result.Deleted != 1 {
		t.Fatalf("same-mode recovery: %+v", result)
	}
	recovered, err := db.Movie(id)
	if err != nil || recovered.Title != "Stack recovered" || !reflect.DeepEqual(before.AdditionalParts, recovered.AdditionalParts) {
		t.Fatalf("stack did not recover in place: %+v err=%v", recovered, err)
	}
	afterPersonal, err = db.DataUser(userID, id)
	if err != nil || !reflect.DeepEqual(personal, afterPersonal) {
		t.Fatalf("recovery changed personal state: %+v err=%v", afterPersonal, err)
	}
	if _, err := db.MovieIDByPath(orphan); !store.NotFound(err) {
		t.Fatalf("complete recovery retained orphan: %v", err)
	}
	t.Logf("actual UID65534/ext4 HTTP stack scan: %s/%s", mode, dependency)
}

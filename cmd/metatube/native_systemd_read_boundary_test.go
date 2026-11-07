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

// Exercise file-input policy through the actual nonprivileged service, not the
// root test process. Local ext4 is the approved first acceptance scope.
func assertNativeSystemdReadBoundaries(t *testing.T, base, state string) {
	t.Helper()
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Post(base+"/Users/AuthenticateByName", "application/json", strings.NewReader(`{"Username":"admin","Pw":"password-1234"}`))
	if err != nil {
		t.Fatal(err)
	}
	var auth struct{ AccessToken string }
	err = json.NewDecoder(response.Body).Decode(&auth)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || auth.AccessToken == "" {
		t.Fatalf("authenticate boundary suite: status=%d err=%v", response.StatusCode, err)
	}
	for _, mode := range []string{"full", "incremental"} {
		for _, name := range []string{"strm-limit", "nfo-limit", "strm-fifo", "nfo-fifo", "symlink-fifo", "strm-denied", "nfo-denied", "regular-symlinks"} {
			t.Run("read-boundary/"+mode+"/"+name, func(t *testing.T) {
				assertNativeSystemdReadBoundary(t, base, state, auth.AccessToken, mode, name)
			})
		}
	}
	assertNativeSystemdStackBoundaries(t, base, state, auth.AccessToken)
}

func assertNativeSystemdReadBoundary(t *testing.T, base, state, token, mode, name string) {
	t.Helper()
	var fs unix.Statfs_t
	if err := unix.Statfs(state, &fs); err != nil || fs.Type != unix.EXT4_SUPER_MAGIC {
		t.Fatalf("read-boundary fixture requires local ext4: type=%x err=%v", fs.Type, err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	call := func(method, path, body string, want int) []byte {
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
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d err=%v body=%s", method, path, resp.StatusCode, want, err, raw)
		}
		return raw
	}
	db, err := store.Open(filepath.Join(state, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	media := filepath.Join(state, "boundary-"+mode+"-"+name)
	if err := os.Mkdir(media, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(media, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	source, meta, orphan := filepath.Join(media, "A.strm"), filepath.Join(media, "A.nfo"), filepath.Join(media, "Z.strm")
	const sourceBody = "https://example.invalid/original.mp4\n"
	const metaBody = "<movie><title>Boundary original</title><plot>Retained plot</plot><actor><name>Boundary actor</name><role>Original role</role></actor></movie>"
	write(source, sourceBody)
	write(meta, metaBody)
	write(orphan, "https://example.invalid/orphan.mp4\n")
	body, err := json.Marshal(map[string]string{"Name": name, "Path": media})
	if err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, "/api/admin/libraries", string(body), http.StatusOK)
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
		t.Fatal("created library missing from database")
	}
	scanPath := fmt.Sprintf("/api/admin/scan?library_id=%d&mode=full", libraryID)
	var baseline scanner.Result
	if raw := call(http.MethodPost, scanPath, "", http.StatusOK); json.Unmarshal(raw, &baseline) != nil || baseline.Success != 1 || baseline.Pending != 1 || !baseline.CleanupPerformed {
		t.Fatalf("initial scan: %s", raw)
	}
	scanPath = fmt.Sprintf("/api/admin/scan?library_id=%d&mode=%s", libraryID, mode)
	id, err := db.MovieIDByPath(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SavePlaybackForUser(1, id, 7654321, 3, "2026-11-06T00:00:00Z", 7654321); err != nil {
		t.Fatal(err)
	}
	userData, err := db.SetIndexedFavoriteDataForUser(1, id, true)
	if err != nil || !userData.IsFavorite || userData.PositionTicks != 7654321 || userData.PlayCount != 3 {
		t.Fatalf("nonempty personal baseline: %+v err=%v", userData, err)
	}
	before, err := db.Movie(id)
	if err != nil || before.Title != "Boundary original" {
		t.Fatalf("initial movie=%+v err=%v", before, err)
	}
	actors, err := db.Actors(id)
	if err != nil || len(actors) != 1 || actors[0].Name != "Boundary actor" {
		t.Fatalf("initial actors=%+v err=%v", actors, err)
	}
	groups, err := db.LoadScanGroups(libraryID)
	if err != nil || len(groups) != 2 {
		t.Fatalf("initial group baselines=%+v err=%v", groups, err)
	}
	// These are fixed fixture paths just created under this unique state
	// directory. Refuse any removal outside this case's media directory.
	if filepath.Dir(orphan) != media || filepath.Dir(source) != media || filepath.Dir(meta) != media {
		t.Fatal("unexpected fixture replacement path")
	}
	if err := os.Remove(orphan); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(media, "B.strm"), "https://example.invalid/healthy.mp4\n")
	write(filepath.Join(media, "B.nfo"), "<movie><title>Healthy boundary</title></movie>")
	target := source
	if strings.HasPrefix(name, "nfo-") {
		target = meta
	}
	switch name {
	case "strm-limit":
		write(source, strings.Repeat("x", (1<<20)+1))
	case "nfo-limit":
		write(meta, strings.Repeat("x", (8<<20)+1))
	case "strm-fifo", "nfo-fifo", "symlink-fifo":
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		fifo := target
		if name == "symlink-fifo" {
			fifo = filepath.Join(media, "input.pipe")
			if err := os.Symlink(fifo, target); err != nil {
				t.Fatal(err)
			}
		}
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(fifo, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	case "strm-denied", "nfo-denied":
		if err := os.Chown(target, 0, 0); err != nil {
			t.Fatal(err)
		}
	case "regular-symlinks":
		for path, data := range map[string]string{source: sourceBody, meta: strings.Replace(metaBody, "Boundary original", "Boundary regular link", 1)} {
			write(path+".input", data)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".input", path); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := http.StatusOK
	if strings.HasSuffix(name, "-denied") {
		want = http.StatusInternalServerError
	}
	if want == http.StatusInternalServerError {
		var permissions unix.Stat_t
		if err := unix.Stat(target, &permissions); err != nil {
			t.Fatal(err)
		}
		if permissions.Uid != 0 || permissions.Mode&0o777 != 0o600 {
			t.Fatalf("permission fixture must be root-owned 0600: %+v", permissions)
		}
	}
	scanRaw := call(http.MethodPost, scanPath, "", want)
	if want == http.StatusInternalServerError && !strings.Contains(string(scanRaw), "scan incomplete: source read failed") {
		t.Fatalf("expected service file-read failure: %s", scanRaw)
	}
	// Failed synchronous scans return an error envelope; the progress
	// endpoint retains the merged partial result for that finished run.
	var progress struct {
		scanner.Result
		Running bool
	}
	raw := call(http.MethodGet, "/api/admin/scan/progress", "", http.StatusOK)
	if err := json.Unmarshal(raw, &progress); err != nil || progress.Running {
		t.Fatalf("finished scan progress: %v %s", err, raw)
	}
	if want == http.StatusOK {
		// The successful response carries final cleanup counters; progress
		// snapshots are emitted before that final cleanup phase.
		if err := json.Unmarshal(scanRaw, &progress.Result); err != nil {
			t.Fatalf("scan result: %v %s", err, scanRaw)
		}
	}
	healthyID, err := db.MovieIDByPath(filepath.Join(media, "B.strm"))
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := db.Movie(healthyID)
	if err != nil || healthy.Title != "Healthy boundary" {
		t.Fatalf("healthy group was not committed during fault scan: %+v err=%v", healthy, err)
	}
	if name == "regular-symlinks" {
		if progress.Success != 2 || progress.Incomplete || !progress.CleanupPerformed || progress.Deleted != 1 {
			t.Fatalf("ordinary file symlinks rejected: %s", raw)
		}
	} else {
		if !progress.Incomplete || progress.CleanupPerformed || progress.Deleted != 0 || progress.Success != 1 || progress.UnreadableNFO != 0 || (want == http.StatusOK && (progress.Skipped != 1 || progress.Failed != 0)) || (want == http.StatusInternalServerError && progress.Failed != 1) {
			t.Fatalf("fault lost partial result/cleanup protection: %s", raw)
		}
		after, err := db.Movie(id)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("fault changed old Movie: before=%+v after=%+v err=%v", before, after, err)
		}
		afterData, err := db.DataUser(1, id)
		if err != nil || !reflect.DeepEqual(userData, afterData) {
			t.Fatalf("fault changed personal state: before=%+v after=%+v err=%v", userData, afterData, err)
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
				t.Fatalf("fault changed committed group %s", key)
			}
		}
		if _, err := db.MovieIDByPath(orphan); err != nil {
			t.Fatalf("incomplete inventory deleted orphan: %v", err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		data := sourceBody
		if target == meta {
			data = metaBody
		}
		write(target, data)
		write(meta, strings.Replace(metaBody, "Boundary original", "Boundary repaired", 1))
		var recovered scanner.Result
		raw = call(http.MethodPost, scanPath, "", http.StatusOK)
		wantSuccess, wantUnchanged := 2, 0
		if mode == "incremental" {
			wantSuccess, wantUnchanged = 1, 1
		}
		if err := json.Unmarshal(raw, &recovered); err != nil || recovered.Success != wantSuccess || recovered.UnchangedGroups != wantUnchanged || recovered.Failed != 0 || recovered.Incomplete || !recovered.CleanupPerformed || recovered.Deleted != 1 {
			t.Fatalf("repaired scan: %v %s", err, raw)
		}
	}
	if _, err := db.MovieIDByPath(orphan); !store.NotFound(err) {
		t.Fatalf("complete inventory did not remove orphan: %v", err)
	}
	wantTitle := "Boundary repaired"
	if name == "regular-symlinks" {
		wantTitle = "Boundary regular link"
	}
	after, err := db.Movie(id)
	if err != nil || after.Title != wantTitle {
		t.Fatalf("recovered metadata not indexed: %+v err=%v", after, err)
	}
	afterData, err := db.DataUser(1, id)
	if err != nil || !reflect.DeepEqual(userData, afterData) {
		t.Fatalf("recovery changed personal state: before=%+v after=%+v err=%v", userData, afterData, err)
	}
	t.Logf("actual UID65534/ext4 HTTP %s scan: %s, partial protection, personal state and changed recovery verified", mode, name)
}

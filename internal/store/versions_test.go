package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func versionStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.waitFeaturesReady()
	t.Cleanup(func() { s.Close() })
	return s
}

func storedVersion(t *testing.T, s *Store, key string) string {
	t.Helper()
	var value string
	err := s.db.QueryRow("SELECT value FROM kv WHERE key=?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestVersionPublishesExactCommittedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versions.db")
	a := versionStore(t, path)
	b := versionStore(t, path)
	// Simulate a mirror that missed another handle's writes. This does not
	// promise multi-process read coherence: only the next writer catches up.
	for i, s := range []*Store{a, b, a, b} {
		if err := s.BumpVersion(7); err != nil {
			t.Fatal(err)
		}
		want := strconv.Itoa(i + 1)
		if got := s.Version("g:version"); got != want {
			t.Fatalf("writer mirror=%s want exact committed %s", got, want)
		}
		if got := storedVersion(t, s, "lib:7:version"); got != want {
			t.Fatalf("library version=%s want %s", got, want)
		}
	}
	a.Close()
	b.Close()
	reopened := versionStore(t, path)
	if reopened.Version("g:version") != "4" {
		t.Fatal("reopen did not load committed version")
	}
}

func TestVersionLibraryFailureRollsBackBothDomains(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(strconv.FormatBool(exists), func(t *testing.T) {
			s := versionStore(t, filepath.Join(t.TempDir(), "rollback.db"))
			if exists {
				if err := s.BumpVersion(7); err != nil {
					t.Fatal(err)
				}
			}
			global, library, mirror := storedVersion(t, s, "g:version"), storedVersion(t, s, "lib:7:version"), s.Version("g:version")
			_, err := s.db.Exec(`CREATE TRIGGER reject_version BEFORE INSERT ON kv
				WHEN NEW.key='lib:7:version' BEGIN SELECT RAISE(ABORT,'fixture version write rejected'); END`)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.BumpVersion(7); err == nil || !strings.Contains(err.Error(), "fixture version write rejected") {
				t.Fatalf("expected fault, got %v", err)
			}
			if storedVersion(t, s, "g:version") != global || storedVersion(t, s, "lib:7:version") != library || s.Version("g:version") != mirror {
				t.Fatal("failed transaction changed a version domain or memory")
			}
			if _, err := s.db.Exec("DROP TRIGGER reject_version"); err != nil {
				t.Fatal(err)
			}
			if err := s.BumpVersion(7); err != nil {
				t.Fatal(err)
			}
			if s.Version("g:version") != storedVersion(t, s, "g:version") {
				t.Fatal("retry published a different version")
			}
		})
	}
}

func TestVersionTxDoesNotPublishBeforeCommit(t *testing.T) {
	s := versionStore(t, filepath.Join(t.TempDir(), "tx.db"))
	for _, commit := range []bool{false, true} {
		tx, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec("INSERT INTO kv(key,value) VALUES('fixture:business','changed')"); err != nil {
			t.Fatal(err)
		}
		version, err := bumpVersionTx(tx, 7)
		if err != nil {
			t.Fatal(err)
		}
		if version != 1 || s.Version("g:version") != "0" {
			t.Fatal("tentative version was published")
		}
		if !commit {
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"fixture:business", "g:version", "lib:7:version"} {
				if storedVersion(t, s, key) != "" {
					t.Fatalf("rollback persisted %s", key)
				}
			}
			continue
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if s.Version("g:version") != "0" {
			t.Fatal("helper published without explicit post-commit step")
		}
		s.publishVersion(version)
		if s.Version("g:version") != "1" || storedVersion(t, s, "g:version") != "1" || storedVersion(t, s, "lib:7:version") != "1" || storedVersion(t, s, "fixture:business") != "changed" {
			t.Fatal("committed versions and fixture business write disagree")
		}
	}
}

func TestVersionPublicationNeverRegresses(t *testing.T) {
	var s Store
	s.publishVersion(12)
	s.publishVersion(11) // Older commit's publisher resumed after the newer one.
	if s.Version("g:version") != "12" {
		t.Fatal("out-of-order publication regressed the mirror")
	}
	var wg sync.WaitGroup
	for n := int64(1); n <= 100; n++ {
		wg.Add(1)
		go func(n int64) { defer wg.Done(); s.publishVersion(n) }(n)
	}
	wg.Wait()
	if s.Version("g:version") != "100" {
		t.Fatal("concurrent publication did not retain maximum")
	}
}

func TestVersionConcurrentBumpsKeepDomainsConsistent(t *testing.T) {
	s := versionStore(t, filepath.Join(t.TempDir(), "concurrent.db"))
	const writers = 32
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- s.BumpVersion(7) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := strconv.Itoa(writers)
	if s.Version("g:version") != want || storedVersion(t, s, "g:version") != want || storedVersion(t, s, "lib:7:version") != want {
		t.Fatal("committed domain counts and mirror diverged")
	}
}

func TestVersionCommitFailureDoesNotPublish(t *testing.T) {
	s := versionStore(t, filepath.Join(t.TempDir(), "commit-failure.db"))
	s.db.SetMaxOpenConns(1) // Force reuse: a poisoned connection cannot hide in the pool.
	if err := s.BumpVersion(7); err != nil {
		t.Fatal(err)
	}
	_, err := s.db.Exec(`CREATE TABLE fixture_parent(id INTEGER PRIMARY KEY);
		CREATE TABLE fixture_child(parent_id INTEGER REFERENCES fixture_parent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER reject_version_commit AFTER INSERT ON kv WHEN NEW.key='lib:9:version'
		BEGIN INSERT INTO fixture_child(parent_id) VALUES(999); END`)
	if err != nil {
		t.Fatal(err)
	}
	// The deferred constraint permits BOTH version statements, then rejects
	// COMMIT. A direct transaction proves the fixture fails at that boundary.
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if version, err := bumpVersionTx(tx, 9); err != nil || version != 2 {
		t.Fatalf("fixture failed before commit: version=%d err=%v", version, err)
	}
	if err := tx.Commit(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("fixture commit should fail, got %v", err)
	}
	// Explicitly clean up the raw fixture; the wrapper below must do this itself.
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpVersion(9); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("wrapper should fail at commit, got %v", err)
	}
	if storedVersion(t, s, "g:version") != "1" || storedVersion(t, s, "lib:7:version") != "1" || storedVersion(t, s, "lib:9:version") != "" || s.Version("g:version") != "1" {
		t.Fatal("commit failure persisted a version or published memory")
	}
	var children int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM fixture_child").Scan(&children); err != nil || children != 0 {
		t.Fatalf("failed transaction retained fixture writes: count=%d err=%v", children, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_version_commit"); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpVersion(9); err != nil {
		t.Fatal(err)
	}
	if s.Version("g:version") != "2" || storedVersion(t, s, "lib:9:version") != "1" {
		t.Fatal("retry after commit failure used a wrong version")
	}
}

func TestVersionLibrariesAdvanceIndependently(t *testing.T) {
	s := versionStore(t, filepath.Join(t.TempDir(), "libraries.db"))
	for _, id := range []int64{7, 9, 7, 0} {
		if err := s.BumpVersion(id); err != nil {
			t.Fatal(err)
		}
	}
	if s.Version("g:version") != "4" || storedVersion(t, s, "g:version") != "4" {
		t.Fatal("global version did not count all domains")
	}
	for key, want := range map[string]string{"lib:7:version": "2", "lib:9:version": "1", "lib:0:version": "1", "lib:99:version": ""} {
		if got := storedVersion(t, s, key); got != want {
			t.Fatalf("%s=%s want %s", key, got, want)
		}
	}
}

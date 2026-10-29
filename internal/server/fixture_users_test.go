package server

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"emby-go/internal/config"
	"emby-go/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Only test setup uses cheap hashes. Password creation/update/authentication under
// test must still call the real store/API methods (and their production cost).
// Hashes are immutable; stores, users and credentials are never shared by tests.
var fixturePasswordHashes sync.Map

// seedFixtureUser inserts setup-only users into an already migrated store. The
// first auto-allocated ID is 1; its legacy administrator row is kept consistent
// with InitializeAdministrator when it is an administrator. This is not a
// replacement for tests of initialization, user creation or password updates.
func seedFixtureUser(t *testing.T, a *App, name, password string, admin bool) store.User {
	t.Helper()
	hash, ok := fixturePasswordHashes.Load(password)
	if !ok {
		generated, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		hash, _ = fixturePasswordHashes.LoadOrStore(password, string(generated))
	}
	fixture, err := sql.Open("sqlite", a.cfg.DBPath+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	tx, err := fixture.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.Exec("INSERT INTO users(name,password_hash,is_admin,created_at,updated_at) VALUES(?,?,?,?,?)", name, hash.(string), admin, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id == 1 && admin {
		if _, err := tx.Exec("INSERT INTO administrators(id,username,password_hash,created_at) VALUES(1,?,?,?)", name, hash.(string), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if id == 1 && admin {
		a.setAdminName(name)
	}
	user, err := a.db.UserByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestFixtureUserSetupMaintainsIsolationAndCredentials(t *testing.T) {
	apps := make([]*App, 2)
	for i := range apps {
		path := filepath.Join(t.TempDir(), "fixture.db")
		db, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		})
		apps[i] = &App{db: db, cfg: config.Config{DBPath: path}}
		owner := seedFixtureUser(t, apps[i], "fixture-owner", "fixture-password", true)
		viewer := seedFixtureUser(t, apps[i], "fixture-viewer", "viewer-password", false)
		if owner.ID != 1 || !owner.IsAdmin || viewer.ID != 2 || viewer.IsAdmin {
			t.Fatalf("fixture identities: owner=%+v viewer=%+v", owner, viewer)
		}
		if apps[i].currentAdminName() != owner.Name {
			t.Fatal("owner name was not initialized")
		}
		if got, valid, err := db.AuthenticateUser(owner.Name, "fixture-password"); err != nil || !valid || got.ID != owner.ID {
			t.Fatalf("fixture authentication: %+v %v %v", got, valid, err)
		}
		if _, valid, err := db.AuthenticateUser(owner.Name, "wrong-password"); err != nil || valid {
			t.Fatalf("wrong fixture password: valid=%v err=%v", valid, err)
		}
		fixture, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
		if err != nil {
			t.Fatal(err)
		}
		var name, hash, userHash, journal string
		err = fixture.QueryRow("SELECT a.username,a.password_hash,u.password_hash FROM administrators a JOIN users u ON u.id=a.id WHERE a.id=1").Scan(&name, &hash, &userHash)
		if err == nil {
			err = fixture.QueryRow("PRAGMA journal_mode").Scan(&journal)
		}
		fixture.Close()
		if err != nil {
			t.Fatal(err)
		}
		if name != owner.Name || hash != userHash || journal != "wal" {
			t.Fatalf("fixture legacy compatibility/WAL: name=%q matching_hash=%v journal=%q", name, hash == userHash, journal)
		}
		if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != bcrypt.MinCost {
			t.Fatalf("fixture-only bcrypt cost=%d err=%v", cost, err)
		}
	}
	if err := apps[0].db.SaveAccessTokenForUser("isolated-fixture-token", 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := apps[1].db.ValidateAccessTokenUser("isolated-fixture-token", time.Now(), 0); err != nil || valid {
		t.Fatalf("session leaked between fixture databases: valid=%v err=%v", valid, err)
	}
	if err := apps[0].db.RenameUser(2, "changed-fixture-viewer"); err != nil {
		t.Fatal(err)
	}
	if viewer, err := apps[1].db.UserByID(2); err != nil || viewer.Name != "fixture-viewer" {
		t.Fatalf("user mutation leaked between fixture databases: %+v %v", viewer, err)
	}
}

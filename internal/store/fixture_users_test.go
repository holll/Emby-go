package store

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Only immutable cheap hashes are shared, never stores, accounts or credentials.
// These helpers are for setup outside the behavior under test. Production
// creation/password updates and all tested mutations keep their real work factor.
var fixturePasswordHashes sync.Map

func seedFixtureUser(t *testing.T, s *Store, name, password string, admin bool) User {
	t.Helper()
	return seedFixtureAccount(t, s, name, password, admin, false)
}

// Match InitializeAdministrator's id=1 and legacy-table write only where the
// original fixture initialized an owner. Ordinary CreateUser fixtures must not
// gain legacy state that was absent before the setup optimization.
func seedFixtureAdministrator(t *testing.T, s *Store, name, password string) User {
	t.Helper()
	return seedFixtureAccount(t, s, name, password, true, true)
}

func seedFixtureAccount(t *testing.T, s *Store, name, password string, admin, initialize bool) User {
	t.Helper()
	hash, ok := fixturePasswordHashes.Load(password)
	if !ok {
		generated, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		hash, _ = fixturePasswordHashes.LoadOrStore(password, string(generated))
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	if err := s.writeTx(func(tx *sql.Tx) error {
		query := "INSERT INTO users(name,password_hash,is_admin,created_at,updated_at) VALUES(?,?,?,?,?)"
		if initialize {
			query = "INSERT INTO users(id,name,password_hash,is_admin,created_at,updated_at) VALUES(1,?,?,?,?,?)"
		}
		result, err := tx.Exec(query, name, hash.(string), admin, now, now)
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return err
		}
		if initialize {
			_, err = tx.Exec("INSERT OR IGNORE INTO administrators(id,username,password_hash,created_at) VALUES(1,?,?,?)", name, hash.(string), now)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	user, err := s.UserByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestFixtureUsersPreserveSetupStateAndIsolation(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		name := "create-user"
		if initialize {
			name = "initialize-administrator"
		}
		t.Run(name, func(t *testing.T) {
			stores := []*Store{openUsersStore(t), openUsersStore(t)}
			for _, s := range stores {
				var owner User
				if initialize {
					owner = seedFixtureAdministrator(t, s, "owner", "fixture-password")
				} else {
					owner = seedFixtureUser(t, s, "owner", "fixture-password", true)
				}
				viewer := seedFixtureUser(t, s, "viewer", "viewer-password", false)
				if owner.ID != 1 || !owner.IsAdmin || viewer.ID != 2 || viewer.IsAdmin {
					t.Fatalf("identities: owner=%+v viewer=%+v", owner, viewer)
				}
				if got, err := s.AdministratorName(); err != nil || got != owner.Name {
					t.Fatalf("owner name=%q err=%v", got, err)
				}
				if authenticated, valid, err := s.AuthenticateUser(owner.Name, "fixture-password"); err != nil || !valid || authenticated.ID != owner.ID || authenticated.authGeneration != 1 {
					t.Fatalf("fixture auth=%+v valid=%v err=%v", authenticated, valid, err)
				}
				if _, valid, err := s.AuthenticateUser(owner.Name, "wrong-password"); err != nil || valid {
					t.Fatalf("wrong password valid=%v err=%v", valid, err)
				}
				var hash, journal string
				if err := s.db.QueryRow("SELECT password_hash FROM users WHERE id=1").Scan(&hash); err != nil {
					t.Fatal(err)
				}
				if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != bcrypt.MinCost {
					t.Fatalf("fixture-only cost=%d err=%v", cost, err)
				}
				if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
					t.Fatalf("journal=%q err=%v", journal, err)
				}
				var count int
				if err := s.db.QueryRow("SELECT COUNT(*) FROM administrators").Scan(&count); err != nil {
					t.Fatal(err)
				}
				wantLegacy := 0
				if initialize {
					wantLegacy = 1
					var legacyName, legacyHash string
					if err := s.db.QueryRow("SELECT username,password_hash FROM administrators WHERE id=1").Scan(&legacyName, &legacyHash); err != nil || legacyName != owner.Name || legacyHash != hash {
						t.Fatalf("legacy name=%q hash_matches=%v err=%v", legacyName, legacyHash == hash, err)
					}
				}
				if count != wantLegacy {
					t.Fatalf("legacy rows=%d want=%d", count, wantLegacy)
				}
			}
			if err := stores[0].SaveAccessTokenForUser("fixture-session", 1, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, valid, err := stores[1].ValidateAccessTokenUser("fixture-session", time.Now(), 0); err != nil || valid {
				t.Fatalf("session leaked between databases: valid=%v err=%v", valid, err)
			}
			if err := stores[0].RenameUser(2, "changed-viewer"); err != nil {
				t.Fatal(err)
			}
			if viewer, err := stores[1].UserByID(2); err != nil || viewer.Name != "viewer" {
				t.Fatalf("mutation leaked between databases: %+v %v", viewer, err)
			}
		})
	}
}

package server

import (
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/store"
)

// Fixture passwords are setup metadata, not authentication proofs. Every session
// below is minted from AuthenticateUser and every administrator mutation receives
// the same opaque request proof used by production handlers.
var fixtureCredentials sync.Map
var fixtureCredentialSequence atomic.Uint64

type fixtureCredentialKey struct {
	db *store.Store
	id int64
}

func rememberFixturePassword(t *testing.T, a *App, id int64, password string) {
	t.Helper()
	key := fixtureCredentialKey{a.db, id}
	fixtureCredentials.Store(key, password)
	t.Cleanup(func() { fixtureCredentials.Delete(key) })
}

func initializeFixtureAdministrator(t *testing.T, a *App, name, password string) error {
	t.Helper()
	if err := a.db.InitializeAdministrator(name, password); err != nil {
		return err
	}
	rememberFixturePassword(t, a, 1, password)
	return nil
}

func authenticatedFixtureUser(t *testing.T, a *App, id int64) store.User {
	t.Helper()
	user, err := a.db.UserByID(id)
	if err != nil {
		t.Fatal(err)
	}
	password, ok := fixtureCredentials.Load(fixtureCredentialKey{a.db, id})
	if !ok {
		t.Fatalf("missing fixture password for user %d", id)
	}
	authenticated, valid, err := a.db.AuthenticateUser(user.Name, password.(string))
	if err != nil || !valid {
		t.Fatalf("fixture authentication: valid=%v err=%v", valid, err)
	}
	return authenticated
}

func saveFixtureSession(t *testing.T, a *App, token string, id int64, expiry time.Time) error {
	t.Helper()
	return a.db.SaveAccessTokenForAuthenticatedUser(token, authenticatedFixtureUser(t, a, id), expiry)
}

func fixtureAdminProof(t *testing.T, a *App, id int64) store.User {
	t.Helper()
	token := fmt.Sprintf("fixture-proof-%d", fixtureCredentialSequence.Add(1))
	if err := saveFixtureSession(t, a, token, id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.db.DeleteAccessToken(token) })
	proof, err := a.db.RequestCredentialUser(token, id, true)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func updateFixtureUser(t *testing.T, a *App, id int64, update store.AdminUserUpdate) error {
	t.Helper()
	err := a.db.AdminUpdateUser(1, id, update, fixtureAdminProof(t, a, 1))
	if err == nil && update.Password != nil {
		rememberFixturePassword(t, a, id, *update.Password)
	}
	return err
}

func fixtureValue[T any](value T) *T { return &value }

// This is setup-only userdata, not a substitute for RecordPlaybackEvent tests.
func seedFixturePlayback(t *testing.T, a *App, movieID, position, count int64, lastPlayed string, stopped int64) {
	t.Helper()
	fixture, err := sql.Open("sqlite", a.cfg.DBPath+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.Exec(`INSERT INTO userdata(user_id,movie_id,position_ticks,play_count,last_played_at,last_stopped_ticks) VALUES(1,?,?,?,?,?)`, movieID, position, count, lastPlayed, stopped); err != nil {
		t.Fatal(err)
	}
}

func createFixtureUser(t *testing.T, a *App, name, password string, admin bool) (store.User, error) {
	t.Helper()
	return seedFixtureUser(t, a, name, password, admin), nil
}

func deleteFixtureAPIKey(t *testing.T, a *App, plaintext string) error {
	t.Helper()
	stable, err := a.db.ResolveAPIKeyStableID(plaintext)
	if err != nil {
		return err
	}
	return a.db.AdminDeleteAPIKey(fixtureAdminProof(t, a, 1), store.APIKeyPublicID(stable), 0)
}

func indexFixtureMovie(t *testing.T, a *App, movie store.Movie, size int64, mtime time.Time) (int64, error) {
	t.Helper()
	actors, err := a.db.Actors(movie.ID)
	if err != nil {
		return 0, err
	}
	return a.db.IndexMovie(movie, size, mtime, actors)
}

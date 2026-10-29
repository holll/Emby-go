package artwork

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openArtworkFixtureSQL(t *testing.T, path string) *sql.DB {
	t.Helper()
	// Match store.Open's per-connection timeout: fixture writes can race its
	// asynchronous feature initialization, including on newly pooled connections.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestArtworkFixtureSQLBusyTimeoutPerConnection(t *testing.T) {
	db := openArtworkFixtureSQL(t, filepath.Join(t.TempDir(), "store.db"))
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Hold both connections simultaneously, forcing a newly opened second
	// connection rather than reusing one initialized by a single Exec.
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var timeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if timeout != 10000 {
			t.Fatalf("connection %d busy_timeout=%d, want 10000", i+1, timeout)
		}
	}
}

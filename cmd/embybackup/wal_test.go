package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupCLIOpenWALSnapshot(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "embybackup")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	source := filepath.Join(root, "source.sqlite")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode=%q error=%v", mode, err)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0;
CREATE TABLE libraries(id INTEGER PRIMARY KEY,path TEXT);
CREATE TABLE snapshot_rows(id INTEGER PRIMARY KEY,generation INTEGER NOT NULL);
INSERT INTO snapshot_rows VALUES(1,1),(2,1);`); err != nil {
		t.Fatal(err)
	}
	var busy, frames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
		t.Fatalf("initial checkpoint busy=%d frames=%d checkpointed=%d error=%v", busy, frames, checkpointed, err)
	}
	mainBefore, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	// These committed values must exist only in WAL, not in the main DB copy.
	if _, err := db.Exec("UPDATE snapshot_rows SET generation=2"); err != nil {
		t.Fatal(err)
	}
	walBefore, err := os.ReadFile(source + "-wal")
	if err != nil || len(walBefore) <= 32 {
		t.Fatalf("no committed WAL frames: bytes=%d error=%v", len(walBefore), err)
	}
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, mainBefore) {
		t.Fatalf("committed update was checkpointed into main DB: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE snapshot_rows SET generation=3"); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := tx.QueryRow("SELECT MIN(generation) FROM snapshot_rows").Scan(&pending); err != nil || pending != 3 {
		t.Fatalf("pending writer generation=%d error=%v", pending, err)
	}
	// The writer stays open throughout the separate CLI process. WAL readers
	// must see committed generation 2 without waiting for this transaction.
	outDir := filepath.Join(root, "backups")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--db", source, "--out", outDir, "--nfo=false", "--extra-backdrops=false").CombinedOutput()
	if err != nil || ctx.Err() != nil || !strings.Contains(string(out), "NFO 0 个，额外背景图 0 个") {
		t.Fatalf("CLI with live WAL writer: %v context=%v\n%s", err, ctx.Err(), out)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("backup entries=%v error=%v", entries, err)
	}
	artifact := filepath.Join(outDir, entries[0].Name(), "db.sqlite")
	assertGeneration := func(path string, want int) {
		t.Helper()
		readDB, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer readDB.Close()
		var integrity string
		if err := readDB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("%s integrity=%q error=%v", path, integrity, err)
		}
		var count, low, high int
		if err := readDB.QueryRow("SELECT COUNT(*),MIN(generation),MAX(generation) FROM snapshot_rows").Scan(&count, &low, &high); err != nil || count != 2 || low != want || high != want {
			t.Fatalf("%s rows=%d generations=%d..%d want=%d error=%v", path, count, low, high, want, err)
		}
	}
	assertGeneration(artifact, 2)
	for path, want := range map[string][]byte{source: mainBefore, source + "-wal": walBefore} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("CLI changed source main/WAL bytes: %s: %v", path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertGeneration(source, 3)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Restore only the actual CLI DB artifact to an empty directory. No source
	// main DB, WAL or SHM is supplied; no Store.Open can migrate the snapshot.
	restoreDir := filepath.Join(root, "restored")
	if err := os.Mkdir(restoreDir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(restoreDir, "db.sqlite")
	if err := os.WriteFile(restored, data, 0600); err != nil {
		t.Fatal(err)
	}
	assertGeneration(restored, 2)
	assertGeneration(artifact, 2)
	t.Log("actual CLI captured committed WAL generation 2 while generation 3 was pending; source advanced to 3, standalone restored DB remains 2 with integrity ok; synthetic SQL fixture, not cross-file NFO consistency")
}

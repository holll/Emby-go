package artwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"emby-go/internal/store"
)

func TestNoOverwriteInstallDoesNotClobberLateProducer(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("temp", picture(), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprint(root, "target")
	if err != nil || before.Exists {
		t.Fatalf("%+v %v", before, err)
	}
	// Exactly the dangerous interleaving: the last destination fingerprint is
	// absent, then another process installs a file just before our final syscall.
	original := []byte("external producer owns this file")
	if err := root.WriteFile("target", original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := installPoster(root, "temp", "target", false); !errors.Is(err, store.ErrArtworkChanged) {
		t.Fatal(err)
	}
	after, err := root.ReadFile("target")
	if err != nil || string(after) != string(original) {
		t.Fatalf("clobbered late producer: %q %v", after, err)
	}
	if err := installPoster(root, "temp", "new-target", false); err != nil {
		t.Fatal(err)
	}
	installed, err := root.ReadFile("new-target")
	if err != nil || string(installed) != string(picture()) {
		t.Fatal("exclusive install failed", err)
	}
}

func TestDamagedPrivatePosterRequiresExplicitOverwrite(t *testing.T) {
	db, m, _ := fixture(t)
	path := m.SourcePath + ".poster.jpg"
	original := []byte("not a decoded image, but still user-owned")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), db, Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "i"}, Options{})
	if err == nil || res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatal("damaged private target overwritten", err)
	}
	entries, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".artwork-*"))
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary files leaked", entries, err)
	}
}

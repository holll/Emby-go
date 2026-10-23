package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func markerDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func TestRootMarkerSafety(t *testing.T) {
	for _, scenario := range []string{"match", "missing", "mismatch", "change-final", "oversize", "absolute", "traversal", "symlink", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			root, s, lib := scanFixture(t)
			marker := filepath.Join(root, ".mount-id")
			scanWrite(t, marker, "expected-volume")
			opts := ScanOptions{Mode: ScanModeIncremental, RootMarkerPath: ".mount-id", RootMarkerSHA256: markerDigest("expected-volume")}
			switch scenario {
			case "missing":
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			case "mismatch":
				scanWrite(t, marker, "wrong-volume")
			case "oversize":
				scanWrite(t, marker, strings.Repeat("x", 65537))
			case "absolute":
				opts.RootMarkerPath = marker
			case "traversal":
				opts.RootMarkerPath = "../.mount-id"
			case "symlink":
				outside := filepath.Join(t.TempDir(), "marker")
				scanWrite(t, outside, "expected-volume")
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, marker); err != nil {
					t.Fatal(err)
				}
			case "partial":
				opts.RootMarkerSHA256 = ""
			}
			got, err := ScanWithOptions(context.Background(), s, lib, opts, func(p Progress) {
				if scenario == "change-final" && p.Done == p.Total && p.Done > 0 {
					scanWrite(t, marker, "wrong-volume")
				}
			})
			if scenario == "match" {
				if err != nil || got.UnchangedGroups != 1 || !got.CleanupPerformed {
					t.Fatalf("match %+v %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrRootMarker) || !got.Incomplete || got.CleanupPerformed {
				t.Fatalf("unsafe marker %+v %v", got, err)
			}
		})
	}
}

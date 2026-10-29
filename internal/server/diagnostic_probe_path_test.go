package server

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSafeDiagnosticProbePathRetainsStructureAndBoundsSecrets(t *testing.T) {
	for _, tc := range []struct{ path, want string }{{"/emby/Users/12/Items/99/Images/Primary", "/emby/users/12/items/99/images/primary"}, {"/Branding/Configuration", "/branding/configuration"}} {
		if got := safeDiagnosticProbePath(tc.path); got != tc.want {
			t.Fatalf("%s => %s", tc.path, got)
		}
	}
	one := safeDiagnosticProbePath("/Users/canary-password/Items/canary-token/Unimplemented")
	other := safeDiagnosticProbePath("/Users/canary-password/Items/canary-token/OtherUnimplemented")
	if one == other || one != safeDiagnosticProbePath("/Users/canary-password/Items/canary-token/Unimplemented") || !strings.HasPrefix(one, "/users/[redacted:") || !strings.Contains(one, "/items/[redacted:") || strings.Contains(one, "canary") || strings.Contains(one, "UNMATCHED") {
		t.Fatalf("unsafe or collapsed %s %s", one, other)
	}
	for _, path := range []string{strings.Repeat("/canary-secret", 30), "/" + strings.Repeat("canary", 1000), "/users/12345678901234567890", "/users/a?token=canary", "/users/%2Fcanary%0A"} {
		got := safeDiagnosticProbePath(path)
		if len(got) > 300 || strings.Contains(got, "canary") || strings.Contains(got, "?") || strings.Contains(got, "12345678901234567890") {
			t.Fatalf("boundary %q", got)
		}
	}
	if got := unmatchedDiagnosticPath("/Users/canary-password/Items/99"); got != "/users/[UNMATCHED]" {
		t.Fatalf("request logger policy changed: %s", got)
	}
}

func TestDiagnosticProbeActualSafePathsDistinctAndFloodBounded(t *testing.T) {
	a, db, r := probeRetryFixture(t)
	now := time.Now()
	a.probeNow = func() time.Time { return now }
	for _, path := range []string{"/Users/12/Items/99/Unimplemented?token=canary-query", "/Users/12/Items/99/OtherUnimplemented?api_key=canary-query", "/Users/12/Items/99/Unimplemented?token=changed-secret"} {
		requestUnknown(t, r, path)
	}
	assertProbeRetryState(t, a, db, 2, 2)
	rows, err := db.Query("SELECT path,query,body_preview FROM api_probe ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var path, query, body string
		if err = rows.Scan(&path, &query, &body); err != nil {
			t.Fatal(err)
		}
		count++
		if !strings.HasPrefix(path, "/users/12/items/99/[redacted:") || strings.Contains(path, "UNMATCHED") || strings.Contains(path+query+body, "canary") || strings.Contains(query, "changed-secret") || body != "[OMITTED]" {
			t.Fatalf("unsafe row %q %q %q", path, query, body)
		}
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal(count)
	}
	for i := range 300 {
		requestUnknown(t, r, fmt.Sprintf("/Users/12/canary-unknown-%d", i))
	}
	// Path detail increases key cardinality, but existing write and memory budgets
	// still apply exactly, including no token refill while the clock is frozen.
	assertProbeRetryState(t, a, db, int(probeWriteBurst), int(probeWriteBurst))
}

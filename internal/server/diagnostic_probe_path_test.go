package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticProbeEscapedPathsAndRawQueries(t *testing.T) {
	a, db, r := probeRetryFixture(t)
	targets := []struct{ uri, path, query string }{
		{"/emby/Users/12/Items/99/Unknown%2fAction?z=1&api_key=canary%2Btoken&token=first&token=second", "/emby/Users/12/Items/99/Unknown%2fAction", "z=1&api_key=canary%2Btoken&token=first&token=second"},
		{"/Users/12/Items/99/Unknown%2FAction?token=other", "/Users/12/Items/99/Unknown%2FAction", "token=other"},
		{"/Branding/Unknown?", "/Branding/Unknown", ""},
		{"/Users/canary-password/Unknown?api_key=secret&bad=%ZZ", "/Users/canary-password/Unknown", "api_key=secret&bad=%ZZ"},
		{"/Users/" + strings.Repeat("long-path", 300) + "?token=" + strings.Repeat("long-query", 1000), "/Users/" + strings.Repeat("long-path", 300), "token=" + strings.Repeat("long-query", 1000)},
	}
	for _, tc := range targets {
		requestUnknown(t, r, tc.uri)
	}
	// Query differences do not create additional entries; escaped path spelling does.
	requestUnknown(t, r, targets[0].path+"?token=changed-secret")
	requestUnknown(t, r, "/Users/12/Items/99/Unknown%2fAction?token=separate-case")
	assertProbeRetryState(t, a, db, len(targets)+1, len(targets)+1)
	rows, err := db.Query("SELECT path,query,body_preview FROM api_probe ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := append(targets, struct{ uri, path, query string }{"", "/Users/12/Items/99/Unknown%2fAction", "token=separate-case"})
	count := 0
	for rows.Next() {
		var path, query, body string
		if err := rows.Scan(&path, &query, &body); err != nil {
			t.Fatal(err)
		}
		if count >= len(want) || path != want[count].path || query != want[count].query || body != "[OMITTED]" {
			t.Fatalf("row %d: %q %q %q", count, path, query, body)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != len(want) {
		t.Fatalf("rows=%d want=%d", count, len(want))
	}
	// The HTTP method remains part of the key even for the same escaped path.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", targets[0].uri, nil))
	if w.Code != 404 {
		t.Fatalf("method-specific probe status=%d", w.Code)
	}
	assertProbeRetryState(t, a, db, len(want)+1, len(want)+1)
	var method string
	if err := db.QueryRow("SELECT method FROM api_probe ORDER BY id DESC LIMIT 1").Scan(&method); err != nil || method != "GET" {
		t.Fatalf("method=%q err=%v", method, err)
	}
}

func TestDiagnosticProbeActualPathsDistinctAndFloodBounded(t *testing.T) {
	a, db, r := probeRetryFixture(t)
	now := time.Now()
	a.probeNow = func() time.Time { return now }
	for _, path := range []string{"/Users/12/Items/99/Unimplemented?token=canary-query", "/Users/12/Items/99/OtherUnimplemented?api_key=canary-query", "/Users/12/Items/99/Unimplemented?token=changed-secret"} {
		requestUnknown(t, r, path)
	}
	assertProbeRetryState(t, a, db, 2, 2)
	for i := range 300 {
		requestUnknown(t, r, fmt.Sprintf("/Users/12/canary-unknown-%d", i))
	}
	// Full URL diagnostics do not change the existing write and memory budgets.
	assertProbeRetryState(t, a, db, int(probeWriteBurst), int(probeWriteBurst))
}

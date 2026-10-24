package stabilityrehearsal

import "testing"

// Nullable metadata compatibility must not repair malformed non-NULL JSON or
// remove the diagnostic that lets an operator locate it for explicit repair.
func TestLegacyNullableMetadataRetainsCorruptionDiagnostics(t *testing.T) {
	path, _ := legacyFixture(t)
	db := rawDB(t, path)
	mustExec(t, db, "UPDATE movies SET file_mtime=NULL, genres='{broken' WHERE id=42")
	s := openStore(t, path)
	version := s.Version("g:version")
	m, err := s.Movie(42)
	if err != nil || m.ID != 42 || m.Title != "Legacy A" || len(m.Genres) != 0 {
		t.Fatalf("read=%+v err=%v", m, err)
	}
	report, err := s.CorruptedJSON(20)
	if err != nil || report.Total != 1 || len(report.Items) != 1 || report.Items[0].MovieID != 42 || report.Items[0].Column != "genres" {
		t.Fatalf("corruption diagnostic lost: %+v %v", report, err)
	}
	var raw string
	if err := db.QueryRow("SELECT genres FROM movies WHERE id=42").Scan(&raw); err != nil || raw != "{broken" {
		t.Fatalf("malformed JSON rewritten: %q %v", raw, err)
	}
	if scalar(t, db, "SELECT count(*) FROM movies WHERE id=42 AND file_mtime IS NULL") != 1 || s.Version("g:version") != version {
		t.Fatal("read or diagnostics rewrote nullable data or advanced version")
	}
}

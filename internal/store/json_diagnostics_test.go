package store

import "testing"

// DB-10：损坏 JSON 可定位到影片与列；正常库返回空报告。
func TestCorruptedJSONLocatesMovieAndColumn(t *testing.T) {
	s, libID := newFeatureStore(t)
	id := upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/j1.strm", Status: "success", Title: "坏 JSON",
	})
	if _, err := s.db.Exec("UPDATE movies SET tags=? WHERE id=?", "not-json", id); err != nil {
		t.Fatal(err)
	}
	report, err := s.CorruptedJSON(10)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 1 || len(report.Items) != 1 {
		t.Fatalf("报告 = %+v，期望 1 条", report)
	}
	if report.Items[0].MovieID != id || report.Items[0].Column != "tags" {
		t.Fatalf("定位 = %+v，期望 movie=%d column=tags", report.Items[0], id)
	}

	if _, err := s.db.Exec("UPDATE movies SET tags=? WHERE id=?", `["ok"]`, id); err != nil {
		t.Fatal(err)
	}
	report, err = s.CorruptedJSON(10)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 0 {
		t.Fatalf("修复后仍有损坏: %+v", report)
	}
}

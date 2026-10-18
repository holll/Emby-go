package store

import (
	"errors"
	"testing"
)

// DB-10：回填进度可读，取消后下次启动重跑（不写 ready 标记）。
func TestFeatureBackfillStatusAndCancel(t *testing.T) {
	s, libID := newFeatureStore(t)
	s.waitFeaturesReady()
	for i := 0; i < 3; i++ {
		upsertFixtureMovie(t, s, libID, Movie{
			LibraryID: libID, SourcePath: "/tmp/av/bf" + string(rune('a'+i)) + ".strm",
			Status: "success", Title: "回填" + string(rune('a'+i)),
		})
	}
	if err := s.SetKV("features:ready", ""); err != nil {
		t.Fatal(err)
	}
	s.featuresDone.Store(0)
	if err := s.ensureFeatures(); err != nil {
		t.Fatal(err)
	}
	st := s.FeatureBackfillStatus()
	if st.Running || st.Done != 3 || st.Total != 3 {
		t.Fatalf("回填状态 = %+v，期望 done/total=3 且不在运行", st)
	}

	// 取消：不写 ready 标记，下次启动仍会重跑。
	s2, lib2 := newFeatureStore(t)
	upsertFixtureMovie(t, s2, lib2, Movie{LibraryID: lib2, SourcePath: "/tmp/av/c1.strm", Status: "success", Title: "x"})
	if err := s2.SetKV("features:ready", ""); err != nil {
		t.Fatal(err)
	}
	s2.CancelFeatureBackfill()
	if err := s2.ensureFeatures(); !errors.Is(err, errFeaturesBackfillCanceled) {
		t.Fatalf("取消后 ensureFeatures = %v，期望 errFeaturesBackfillCanceled", err)
	}
}

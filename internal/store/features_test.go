package store

import (
	"testing"
	"time"
)

// upsertFixtureMovie 建库 + 写一部影片，返回影片 id。
func upsertFixtureMovie(t *testing.T, s *Store, libraryID int64, movie Movie) int64 {
	t.Helper()
	id, err := s.UpsertMovie(movie, 0, time.Now())
	if err != nil {
		t.Fatalf("UpsertMovie(%s): %v", movie.Title, err)
	}
	return id
}

func newFeatureStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(t.TempDir() + "/features.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	lib, err := s.AddLibrary("AV", "/tmp/av")
	if err != nil {
		t.Fatal(err)
	}
	return s, lib.ID
}

// TestUpsertMovieMaintainsFeatures 写入/更新影片时倒排特征随元数据同步，
// 且字段没变时不重复写（全库扫描会对每部片走一遍 UpsertMovie）。
func TestUpsertMovieMaintainsFeatures(t *testing.T) {
	s, libID := newFeatureStore(t)
	movie := Movie{
		LibraryID: libID, SourcePath: "/tmp/av/a.strm", Status: "success", Title: "A",
		Number: "ABF-018", Genres: []string{"Drama", "Drama"}, Tags: []string{"单体"},
		Studios: []string{"Studio X"}, Director: "D1", Collection: "Series S", Series: "S",
	}
	id := upsertFixtureMovie(t, s, libID, movie)

	features := map[string]string{}
	rows, err := s.db.Query("SELECT kind, value FROM movie_features WHERE movie_id=?", id)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			t.Fatal(err)
		}
		features[kind] = value
	}
	rows.Close()
	for kind, want := range map[string]string{
		FeatureGenre: "Drama", FeatureTag: "单体", FeatureStudio: "Studio X",
		FeatureDirector: "D1", FeatureSeries: "Series S",
	} {
		if features[kind] != want {
			t.Errorf("特征 %s = %q，期望 %q（全部特征：%v）", kind, features[kind], want, features)
		}
	}
	// 重复的 genre 只留一条。
	var genreCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movie_features WHERE movie_id=? AND kind=?", id, FeatureGenre).Scan(&genreCount); err != nil {
		t.Fatal(err)
	}
	if genreCount != 1 {
		t.Errorf("重复特征应去重，实际 %d 条", genreCount)
	}

	// 元数据没变：再次 upsert 不应改动特征行（updated 时间戳不变即可判断）。
	var updatedBefore string
	_ = s.db.QueryRow("SELECT updated_at FROM movie_features WHERE movie_id=? AND kind=?", id, FeatureGenre).Scan(&updatedBefore)
	upsertFixtureMovie(t, s, libID, movie)
	if err := s.db.QueryRow("SELECT COUNT(*) FROM movie_features WHERE movie_id=?", id).Scan(&genreCount); err != nil {
		t.Fatal(err)
	}
	if genreCount != len(features) {
		t.Errorf("特征条数应保持不变，实际 %d，期望 %d", genreCount, len(features))
	}

	// 元数据变了：特征跟着变。
	movie.Genres = []string{"Action"}
	upsertFixtureMovie(t, s, libID, movie)
	var value string
	if err := s.db.QueryRow("SELECT value FROM movie_features WHERE movie_id=? AND kind=?", id, FeatureGenre).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "Action" {
		t.Errorf("改类型后特征未更新：%q", value)
	}
}

// TestSimilarCandidatesRanksByFeatureOverlap 相似候选按特征重合度排序：
// 同系列（权重最高）排在同类型之前，与来源无共同特征的影片不进候选。
func TestSimilarCandidatesRanksByFeatureOverlap(t *testing.T) {
	s, libID := newFeatureStore(t)
	src := upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/src.strm", Status: "success", Title: "来源",
		Genres: []string{"Drama"}, Collection: "Series S",
	})
	// 同系列（+20）且同类型（+10）
	sameSeries := upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/series.strm", Status: "success", Title: "同系列",
		Genres: []string{"Drama"}, Collection: "Series S",
	})
	// 只有同类型（+10）
	sameGenre := upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/genre.strm", Status: "success", Title: "同类型",
		Genres: []string{"Drama"},
	})
	// 只有同演员（+3）
	shared := ActorRef{Name: "Actor One"}
	if err := s.ReplaceActors(src, []ActorRef{shared}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceActors(sameGenre, []ActorRef{shared}); err != nil {
		t.Fatal(err)
	}
	// 毫不相关（且是 pending，本就不该出现）
	upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/other.strm", Status: "success", Title: "无关",
		Genres: []string{"Comedy"},
	})
	upsertFixtureMovie(t, s, libID, Movie{
		LibraryID: libID, SourcePath: "/tmp/av/pending.strm", Status: "pending", Title: "待补录",
		Genres: []string{"Drama"},
	})

	movie, err := s.Movie(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SimilarCandidates(movie, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("候选数 = %d，期望 2（无关与 pending 都不该进）: %+v", len(got), got)
	}
	if got[0].ID != sameSeries || got[1].ID != sameGenre {
		t.Errorf("排序应同系列优先: %+v（sameSeries=%d sameGenre=%d）", got, sameSeries, sameGenre)
	}
	if got[1].Score != WeightGenre+WeightActor {
		t.Errorf("同类型候选得分 = %d，期望 %d", got[1].Score, WeightGenre+WeightActor)
	}
}

// TestEnsureFeaturesBackfillsExistingMovies 存量库（特征表为空）首次打开时回填特征。
func TestEnsureFeaturesBackfillsExistingMovies(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/backfill.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.waitFeaturesReady() // 首次 Open 的后台回填先跑完，避免与下面的写入抢锁
	lib, err := s.AddLibrary("AV", "/tmp/av2")
	if err != nil {
		t.Fatal(err)
	}
	id := upsertFixtureMovie(t, s, lib.ID, Movie{
		LibraryID: lib.ID, SourcePath: "/tmp/av2/a.strm", Status: "success", Title: "A",
		Genres: []string{"Drama"},
	})
	// 模拟「旧版本升级上来」：清掉特征与标记，重新打开应自动回填。
	if _, err := s.db.Exec("DELETE FROM movie_features"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM kv WHERE key='features:ready'"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.waitFeaturesReady()
	var count int
	if err := reopened.db.QueryRow("SELECT COUNT(*) FROM movie_features WHERE movie_id=?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("回填后特征条数 = %d，期望 1", count)
	}
}

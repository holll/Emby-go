package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// P1-5：扫描提交 / 清单对账必须把调用方的 ctx 一路带到等锁循环。
// internal/scanner/incremental.go:307 把真实的扫描 ctx 交给
// CommitScanGroupCheckedContext，进而 commitScanGroup(ctx, ...) → writeTxContext →
// lockWriter。此前这里用的是 writeTx(= Background)，ctx 取消后写者仍会在
// writeMu 上自旋等前一写事务结束，scanner 的 goroutine 与取消信号彻底脱钩。
func TestScanContextCancellationAbandonsWriterWait(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		s, lib := scanFixture(t)
		// 占住进程内写者，模拟一次长提交。
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan error, 1)
		go func() {
			_, err := s.CommitScanGroupCheckedContext(ctx, lib, "g", "fingerprint", 1, "success",
				[]ScanGroupMovie{scanMovie(lib, "/a.strm")},
				[]ScanGroupSource{{Path: "/a.strm"}})
			done <- err
		}()
		assertContextAbandonsWriterWait(t, "commitScanGroup", done, context.Canceled)
	})
	t.Run("reconcile", func(t *testing.T) {
		s, lib := scanFixture(t)
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := s.ReconcileScanInventoryCheckedContext(ctx, lib, map[string]int64{"/a.strm": 1}, []string{"g"})
			done <- err
		}()
		assertContextAbandonsWriterWait(t, "reconcileScanInventory", done, context.DeadlineExceeded)
	})
}

func assertContextAbandonsWriterWait(t *testing.T, name string, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("%s: err=%v want %v", name, err, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: 已被取消的调用仍在等写锁（ctx 没有到 lockWriter）", name)
	}
}

// P2-7：writeTx 用重跑整个回调来重试 SQLITE_BUSY，所以回调内的累加必须对
// "第几次尝试"幂等。此前每重试一次就把同一批 id 再加一遍，Done 会超过 Total。
func TestFeatureBatchRetryDoesNotDoubleCount(t *testing.T) {
	s, libID := newFeatureStore(t)
	s.waitFeaturesReady()
	for i := range 2 {
		upsertFixtureMovie(t, s, libID, Movie{
			LibraryID: libID, SourcePath: fmt.Sprintf("/f/%d.strm", i), Status: "success",
			Title: fmt.Sprintf("F%d", i), Genres: []string{"Drama"},
		})
	}
	// upsert 已把特征行写成与元数据一致，refreshFeaturesTx 会走"相同则不写"的
	// 快路径而不产生 INSERT；改元数据强制重建，才能让下面的触发器命中。
	if _, err := s.db.Exec("UPDATE movies SET genres='[\"Changed\"]'"); err != nil {
		t.Fatal(err)
	}
	ids := movieIDsByID(t, s)
	if len(ids) != 2 {
		t.Fatalf("ids=%v want 2 movies", ids)
	}
	// 让 ensureFeatures 最后一部片必然撞一次 busy：writeTx 会整批重试。
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE TRIGGER fixture_features_busy BEFORE INSERT ON movie_features
		WHEN NEW.movie_id=%d BEGIN SELECT RAISE(ABORT,'database is locked'); END`, ids[1])); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV("features:ready", ""); err != nil {
		t.Fatal(err)
	}
	err := s.ensureFeatures()
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("ensureFeatures err=%v want a busy failure", err)
	}
	status := s.FeatureBackfillStatus()
	if status.Total != 2 {
		t.Fatalf("total=%d want 2", status.Total)
	}
	if status.Done != 1 {
		t.Fatalf("done=%d after 3 attempts want 1 (每次尝试应回写到已提交的 base)", status.Done)
	}
	if status.Done > status.Total {
		t.Fatalf("done=%d > total=%d", status.Done, status.Total)
	}
	// 去掉故障重跑：本次运行的 Done 必须精确落在 Total 上。
	if _, err := s.db.Exec("DROP TRIGGER fixture_features_busy"); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureFeatures(); err != nil {
		t.Fatalf("ensureFeatures after fault: %v", err)
	}
	status = s.FeatureBackfillStatus()
	if status.Done != 2 || status.Total != 2 {
		t.Fatalf("status=%+v want done=total=2", status)
	}
}

func movieIDsByID(t *testing.T, s *Store) []int64 {
	t.Helper()
	rows, err := s.db.Query("SELECT id FROM movies ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// P2-8：清点 inventory 与删除必须在同一个事务里。此前先无锁读一份 missing，
// 再开事务按路径删；两次写入之间并发 IndexMovie 新建的行会被当垃圾删掉。
// 用测试钩子在"清点后、删除前"由第二个 Store（独立连接池与 writeMu）提交一条
// 新行，旧写法里钩子根本不存在（attempts==0）且新行存活；新写法必须整回调重跑。
func TestDeleteMissingSourcesEnumeratesInsideTransaction(t *testing.T) {
	path := t.TempDir() + "/dms.db"
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.waitFeaturesReady()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	b.waitFeaturesReady()
	lib, err := a.AddLibrary("dms", "/dms")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.IndexMovie(Movie{LibraryID: lib.ID, SourcePath: "/dms/stale.strm", Status: "success", Title: "stale"}, 1, time.Unix(1, 0), nil); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	a.testDeleteMissingSourcesHook = func() error {
		if attempts.Add(1) > 1 {
			return nil
		}
		// 第一次尝试：别人在清点之后提交了一条新行。
		_, err := b.IndexMovie(Movie{LibraryID: lib.ID, SourcePath: "/dms/late.strm", Status: "success", Title: "late"}, 1, time.Unix(1, 0), nil)
		return err
	}
	t.Cleanup(func() { a.testDeleteMissingSourcesHook = nil })
	if err := a.DeleteMissingSources(lib.ID, map[string]struct{}{}); err != nil {
		t.Fatalf("DeleteMissingSources: %v", err)
	}
	if got := attempts.Load(); got < 2 {
		t.Fatalf("attempts=%d want >=2：清点必须和删除一起重跑（旧写法在事务外清点）", got)
	}
	for _, source := range []string{"/dms/stale.strm", "/dms/late.strm"} {
		if n := indexedDeleteCount(t, a, "SELECT COUNT(*) FROM movies WHERE source_path=?", source); n != 0 {
			t.Errorf("%s survived: %d", source, n)
		}
	}
	// legacy 契约：这条路径仍不发布版本（钩子那侧的 IndexMovie 会 bump，所以
	// 摘掉钩子后再调一次，专门看本路径自己的版本副作用）。
	a.testDeleteMissingSourcesHook = nil
	versions := [2]string{storedVersion(t, a, "g:version"), storedVersion(t, a, fmt.Sprintf("lib:%d:version", lib.ID))}
	if err := a.DeleteMissingSources(lib.ID, map[string]struct{}{}); err != nil {
		t.Fatalf("DeleteMissingSources (no hook): %v", err)
	}
	after := [2]string{storedVersion(t, a, "g:version"), storedVersion(t, a, fmt.Sprintf("lib:%d:version", lib.ID))}
	if after != versions {
		t.Fatalf("versions=%v want %v", after, versions)
	}
}

// P2-14：版本与可见计数必须作为一个单元发布。读者按 (version, count) 成对比较，
// 不能出现"看到新版本号 + 旧计数"的组合——那会让客户端在版本已推进的情况下
// 拿到过期条目数，且缓存要等下一轮版本推进才自愈。
func TestVisibleMovieCountSnapshotPublishesPairAtomically(t *testing.T) {
	s, libID := newFeatureStore(t)
	s.waitFeaturesReady()
	// 用 IndexMovie（writeVersioned）而不是 UpsertMovie：后者不 bump 全局版本，
	// 计数变化不会被缓存失效，测不出发布/失效的配对关系。
	indexVisible := func(path, title string) int64 {
		t.Helper()
		id, err := s.IndexMovie(Movie{LibraryID: libID, SourcePath: path, Status: "success", Title: title}, 1, time.Unix(1, 0), nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	var ids []int64
	for i := range 3 {
		ids = append(ids, indexVisible(fmt.Sprintf("/vis/%d.strm", i), fmt.Sprintf("V%d", i)))
	}
	if got := s.visibleMovieCount(); got != 3 {
		t.Fatalf("visible=%d want 3", got)
	}
	version := s.gversion.Load()
	snapshot := s.visible.Load()
	if snapshot == nil || snapshot.version != version || snapshot.count != 3 {
		t.Fatalf("snapshot=%+v version=%d want {version:%d count:3}", snapshot, version, version)
	}
	// 版本不符必须整体 miss，绝不能拿旧计数配新版本。
	if count, ok := cachedVisibleCount(snapshot, version-1); ok {
		t.Fatalf("旧版本号命中了新计数 %d", count)
	}
	if count, ok := cachedVisibleCount(snapshot, version); !ok || count != 3 {
		t.Fatalf("同版本缓存 count=%d ok=%v want 3", count, ok)
	}

	// 删一部（bump 版本）：下一次读取发布成对的 (新版本, 新计数)。
	if err := s.DeleteIndexedMovie(ids[0]); err != nil {
		t.Fatal(err)
	}
	if got := s.visibleMovieCount(); got != 2 {
		t.Fatalf("visible after delete=%d want 2", got)
	}
	newVersion := s.gversion.Load()
	if newVersion == version {
		t.Fatalf("delete did not bump the version (%d)", version)
	}
	snapshot = s.visible.Load()
	if snapshot == nil || snapshot.version != newVersion || snapshot.count != 2 {
		t.Fatalf("snapshot=%+v version=%d want {version:%d count:2}", snapshot, newVersion, newVersion)
	}

	// -race 压测：写者持续 bump 版本，读者反复走缓存/重算路径。所有读数必须落在
	// 合法区间，且提交完毕后必须收敛到数据库真值（不靠 sleep，靠最终读断言）。
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if count := s.visibleMovieCount(); count < 1 || count > 32 {
					t.Errorf("visibleMovieCount=%d out of range", count)
					return
				}
			}
		}()
	}
	for i := range 12 {
		indexVisible(fmt.Sprintf("/vis/race-%d.strm", i), fmt.Sprintf("R%d", i))
	}
	close(stop)
	wg.Wait()
	want := indexedDeleteCount(t, s, "SELECT COUNT(*) FROM movies WHERE status IN ('success','manual')")
	if got := s.visibleMovieCount(); got != want {
		t.Fatalf("final visible=%d want %d gversion=%d snapshot=%+v", got, want, s.gversion.Load(), s.visible.Load())
	}
	snapshot = s.visible.Load()
	if snapshot == nil || snapshot.version != s.gversion.Load() || snapshot.count != int64(want) {
		t.Fatalf("final snapshot=%+v version=%d count=%d", snapshot, s.gversion.Load(), want)
	}
}

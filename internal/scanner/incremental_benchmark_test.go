package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"emby-go/internal/store"
)

// BenchmarkScanComparison is a reproducible, disposable local workload. It does
// not touch configured libraries or remote mounts. Run with -benchmem -benchtime=1x.
func BenchmarkScanComparison(b *testing.B) {
	for _, cd := range []bool{false, true} {
		for _, percent := range []int{0, 1, 10} {
			for _, mode := range []ScanMode{ScanModeFull, ScanModeIncremental} {
				b.Run(fmt.Sprintf("cd=%t/change=%d/%s", cd, percent, mode), func(b *testing.B) {
					const count = 200
					root := b.TempDir()
					sources := make([]string, count)
					nfos := make([]string, count)
					for i := 0; i < count; i++ {
						base := fmt.Sprintf("Film-%03d", i)
						sourceBase := base
						if cd {
							sourceBase += "-CD1"
						}
						sources[i] = filepath.Join(root, sourceBase+".strm")
						nfos[i] = filepath.Join(root, sourceBase+".nfo")
						scanWrite(b, sources[i], "https://media.test/a.mp4")
						if cd {
							scanWrite(b, filepath.Join(root, base+"-CD2.strm"), "https://media.test/b.mp4")
						}
						scanWrite(b, nfos[i], "<movie><title>Before</title><genre>Genre</genre><actor><name>Actor</name></actor></movie>")
					}
					s, err := store.Open(filepath.Join(root, "bench.db"))
					if err != nil {
						b.Fatal(err)
					}
					defer s.Close()
					lib, err := s.AddLibrary("benchmark", root)
					if err != nil {
						b.Fatal(err)
					}
					if _, err := Scan(s, lib); err != nil {
						b.Fatal(err)
					}
					db, err := sql.Open("sqlite", filepath.Join(root, "bench.db")+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
					if err != nil {
						b.Fatal(err)
					}
					defer db.Close()
					if _, err := db.Exec("CREATE TABLE bench_dml (domain TEXT PRIMARY KEY, count INTEGER NOT NULL)"); err != nil {
						b.Fatal(err)
					}
					for _, domain := range []string{"business", "state"} {
						if _, err := db.Exec("INSERT INTO bench_dml VALUES (?,0)", domain); err != nil {
							b.Fatal(err)
						}
					}
					for _, table := range []string{"movies", "actors", "movie_actors", "movie_features", "scan_groups", "scan_group_sources"} {
						domain := "business"
						if table == "scan_groups" || table == "scan_group_sources" {
							domain = "state"
						}
						for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
							query := fmt.Sprintf("CREATE TRIGGER count_%s_%s AFTER %s ON %s BEGIN UPDATE bench_dml SET count=count+1 WHERE domain='%s'; END", table, op, op, table, domain)
							if _, err := db.Exec(query); err != nil {
								b.Fatal(err)
							}
						}
					}
					var business, state, rebuilt, versions int64
					b.ReportAllocs()
					b.ResetTimer()
					for iteration := 0; iteration < b.N; iteration++ {
						b.StopTimer()
						for i := 0; i < count*percent/100; i++ {
							scanWrite(b, nfos[i], fmt.Sprintf("<movie><title>Changed-%d</title><genre>Genre</genre><actor><name>Actor</name></actor></movie>", iteration))
						}
						if _, err := db.Exec("UPDATE bench_dml SET count=0"); err != nil {
							b.Fatal(err)
						}
						before, _ := strconv.ParseInt(s.Version("g:version"), 10, 64)
						b.StartTimer()
						got, err := ScanWithOptions(context.Background(), s, lib, ScanOptions{Mode: mode}, nil)
						b.StopTimer()
						if err != nil {
							b.Fatal(err)
						}
						var n int64
						if err := db.QueryRow("SELECT count FROM bench_dml WHERE domain='business'").Scan(&n); err != nil {
							b.Fatal(err)
						}
						business += n
						if err := db.QueryRow("SELECT count FROM bench_dml WHERE domain='state'").Scan(&n); err != nil {
							b.Fatal(err)
						}
						state += n
						rebuilt += int64(got.GroupsDone - got.UnchangedGroups)
						after, _ := strconv.ParseInt(s.Version("g:version"), 10, 64)
						versions += after - before
						if mode == ScanModeIncremental && percent == 0 && (business != 0 || state != 0 || after != before) {
							b.Fatalf("unchanged writes: %+v business=%d state=%d", got, business, state)
						}
						b.StartTimer()
					}
					b.StopTimer()
					b.ReportMetric(float64(business)/float64(b.N), "business-DML/op")
					b.ReportMetric(float64(state)/float64(b.N), "state-DML/op")
					b.ReportMetric(float64(rebuilt)/float64(b.N), "built-groups/op")
					b.ReportMetric(float64(versions)/float64(b.N), "version-delta/op")
				})
			}
		}
	}
}

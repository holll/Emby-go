package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/scanner"
)

// BenchmarkStabilityBrowseDuringScan is an opt-in local-disk load experiment,
// not a production SLO. Reported heap high-water is sampled live Go heap of this
// process, not RSS, exact peak memory, or a per-stage allocation measurement.
func BenchmarkStabilityBrowseDuringScan(b *testing.B) {
	for _, mode := range []scanner.ScanMode{scanner.ScanModeFull, scanner.ScanModeIncremental} {
		b.Run(string(mode), func(b *testing.B) {
			root := b.TempDir()
			media := filepath.Join(root, "media")
			if err := os.Mkdir(media, 0755); err != nil {
				b.Fatal(err)
			}
			for i := range 200 {
				base := filepath.Join(media, fmt.Sprintf("S-%04d", i))
				if err := os.WriteFile(base+".strm", []byte("https://media.invalid/sample.mp4\n"), 0600); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(base+".nfo", []byte(fmt.Sprintf("<movie><title>Sample %d</title><genre>Stability</genre></movie>", i)), 0600); err != nil {
					b.Fatal(err)
				}
			}
			app, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
			if err != nil {
				b.Fatal(err)
			}
			defer app.Close()
			if err := app.db.InitializeAdministrator("load-admin", "local-load-password"); err != nil {
				b.Fatal(err)
			}
			user, valid, err := app.db.AuthenticateUser("load-admin", "local-load-password")
			if err != nil || !valid {
				b.Fatalf("benchmark identity: valid=%v err=%v", valid, err)
			}
			const token = "isolated-benchmark-session"
			if err := app.db.SaveAccessTokenForAuthenticatedUser(token, user, time.Now().Add(time.Hour)); err != nil {
				b.Fatal(err)
			}
			lib, err := app.db.AddLibrary("Load fixture", media)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := scanner.ScanWithOptions(context.Background(), app.db, lib, scanner.ScanOptions{Mode: scanner.ScanModeIncremental}, nil); err != nil {
				b.Fatal(err)
			}
			srv := httptest.NewServer(app.Handler())
			defer srv.Close()
			client := &http.Client{Timeout: 10 * time.Second}
			browse := func() error {
				req, err := http.NewRequest(http.MethodGet, srv.URL+"/Items?Limit=20", nil)
				if err != nil {
					return err
				}
				req.Header.Set("X-Emby-Token", token)
				response, err := client.Do(req)
				if err != nil {
					return err
				}
				_, readErr := io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode != 200 {
					return fmt.Errorf("browse status %d", response.StatusCode)
				}
				return readErr
			}
			if err := browse(); err != nil {
				b.Fatal(err)
			}
			var latencies []time.Duration
			var scans time.Duration
			var highwater uint64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				stop := make(chan struct{})
				done := make(chan struct{})
				failures := make(chan error, 1)
				go func() {
					defer close(done)
					for {
						select {
						case <-stop:
							return
						default:
						}
						start := time.Now()
						if err := browse(); err != nil {
							failures <- err
							return
						}
						latencies = append(latencies, time.Since(start))
					}
				}()
				heapDone := make(chan uint64, 1)
				go func() {
					ticker := time.NewTicker(2 * time.Millisecond)
					defer ticker.Stop()
					var peak uint64
					sample := func() {
						var m runtime.MemStats
						runtime.ReadMemStats(&m)
						if m.HeapAlloc > peak {
							peak = m.HeapAlloc
						}
					}
					sample()
					for {
						select {
						case <-ticker.C:
							sample()
						case <-stop:
							sample()
							heapDone <- peak
							return
						}
					}
				}()
				start := time.Now()
				_, scanErr := scanner.ScanWithOptions(context.Background(), app.db, lib, scanner.ScanOptions{Mode: mode}, nil)
				scans += time.Since(start)
				close(stop)
				<-done
				peak := <-heapDone
				if peak > highwater {
					highwater = peak
				}
				if scanErr != nil {
					b.Fatal(scanErr)
				}
				select {
				case err := <-failures:
					b.Fatal(err)
				default:
				}
			}
			b.StopTimer()
			if len(latencies) == 0 {
				b.Fatal("no concurrent browse samples")
			}
			slices.Sort(latencies)
			b.ReportMetric(float64(scans.Nanoseconds())/float64(b.N)/1e6, "scan-ms/op")
			b.ReportMetric(float64(latencies[(len(latencies)-1)/2].Nanoseconds())/1e6, "browse-p50-ms")
			b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100].Nanoseconds())/1e6, "browse-p95-ms")
			b.ReportMetric(float64(len(latencies))/float64(b.N), "browse-samples/op")
			b.ReportMetric(float64(highwater)/(1024*1024), "sampled-heap-MiB")
		})
	}
}

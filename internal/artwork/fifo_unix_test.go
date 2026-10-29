//go:build unix

package artwork

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFIFOPathsRejectedWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"source", "target", "indexed"} {
		t.Run(kind, func(t *testing.T) {
			db, m, dir := fixture(t)
			path := m.SourcePath + ".poster.jpg"
			if kind == "source" {
				path = m.SourcePath
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "indexed" {
				path = filepath.Join(dir, "poster.jpg")
				if err := db.SetIndexedImage(m.ID, "poster", path); err != nil {
					t.Fatal(err)
				}
			}
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			c := provider(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("downloaded before rejecting FIFO")
				w.Write(picture())
			})
			done := make(chan error, 1)
			go func() {
				_, err := Run(context.Background(), db, Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "i"}, Options{Client: c})
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("accepted FIFO")
				}
			case <-time.After(time.Second):
				// Unblock the old implementation so cleanup cannot hang after regression.
				f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == nil {
					f.Close()
				}
				t.Fatal("FIFO open blocked artwork")
			}
		})
	}
}

package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Clients discover preview availability through ThumbnailSet independently of
// PlaybackInfo, then construct the Videos/{id}/index.bif URL themselves.
func TestBIFDiscoveryIndependentOfPlaybackInfo(t *testing.T) {
	f := artworkFixture(t)
	movie, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	movie.SourceProtocol = "https"
	if _, err := f.a.db.IndexMovie(movie, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}

	for _, available := range []bool{false, true} {
		if available {
			spriteSeed(t, f)
		}
		for _, prefix := range []string{"", "/emby"} {
			t.Run(fmt.Sprintf("preview=%t/prefix=%s", available, prefix), func(t *testing.T) {
				for _, method := range []string{http.MethodGet, http.MethodPost} {
					path := fmt.Sprintf("%s/Items/%d/PlaybackInfo", prefix, f.movie)
					w := translationCall(f.a, f.token, method, path, `{}`)
					if w.Code != http.StatusOK {
						t.Fatalf("%s PlaybackInfo: status %d", method, w.Code)
					}
					var response map[string]json.RawMessage
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					// PlaybackInfoResponse 按文档只有 MediaSources / PlaySessionId / ErrorCode
					// 三个字段，多一个都说明混进了不该有的预览信息。
					if len(response) != 3 || response["MediaSources"] == nil || response["PlaySessionId"] == nil || response["ErrorCode"] == nil {
						t.Fatalf("%s PlaybackInfo must keep its media/session envelope, without invented preview fields: keys %v", method, response)
					}
					var sources []struct{ Id string }
					var session string
					if err := json.Unmarshal(response["MediaSources"], &sources); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(response["PlaySessionId"], &session); err != nil {
						t.Fatal(err)
					}
					if len(sources) != 1 || sources[0].Id == "" || session == "" {
						t.Fatalf("%s PlaybackInfo has no usable media source/session", method)
					}
				}

				setPath := fmt.Sprintf("%s/Items/%d/ThumbnailSet?Width=320", prefix, f.movie)
				set := translationCall(f.a, f.token, http.MethodGet, setPath, "")
				if !available {
					if set.Code != http.StatusNotFound {
						t.Fatalf("unpublished ThumbnailSet: status %d", set.Code)
					}
					return
				}
				var thumbnails struct {
					AspectRatio float64
					Thumbnails  []struct {
						PositionTicks int64
						ImageTag      string
					}
				}
				if set.Code != http.StatusOK {
					t.Fatalf("published ThumbnailSet: status %d", set.Code)
				}
				if err := json.Unmarshal(set.Body.Bytes(), &thumbnails); err != nil {
					t.Fatal(err)
				}
				if thumbnails.AspectRatio <= 0 || len(thumbnails.Thumbnails) != 2 || thumbnails.Thumbnails[0].ImageTag == "" || thumbnails.Thumbnails[0].PositionTicks != 0 || thumbnails.Thumbnails[1].PositionTicks != 100000000 {
					t.Fatalf("invalid published ThumbnailSet: %+v", thumbnails)
				}

				bifPath := fmt.Sprintf("%s/Videos/%d/index.bif?Width=320", prefix, f.movie)
				bif := translationCall(f.a, f.token, http.MethodGet, bifPath, "")
				data := bif.Body.Bytes()
				const indexEnd = 64 + 3*8 // Two frames and the terminal index entry.
				if bif.Code != http.StatusOK || len(data) <= indexEnd {
					t.Fatalf("discovered BIF: status %d, length %d", bif.Code, len(data))
				}
				sidecar := strings.TrimSuffix(movie.NFOPath, filepath.Ext(movie.NFOPath)) + ".bif"
				saved, err := os.ReadFile(sidecar)
				if err != nil || !bytes.Equal(saved, data) {
					t.Fatalf("HTTP BIF differs from the NFO-adjacent sidecar: %v", err)
				}
				if !bytes.Equal(data[:8], []byte{0x89, 'B', 'I', 'F', 13, 10, 26, 10}) || binary.LittleEndian.Uint32(data[8:12]) != 0 || binary.LittleEndian.Uint32(data[12:16]) != 2 || binary.LittleEndian.Uint32(data[16:20]) != 1 {
					t.Fatal("invalid BIF magic/version/frame count/timestamp multiplier")
				}
				for i, timestamp := range []uint32{0, 10000, ^uint32(0)} {
					entry := data[64+i*8 : 72+i*8]
					if binary.LittleEndian.Uint32(entry[:4]) != timestamp {
						t.Fatalf("BIF timestamp %d does not match ThumbnailSet", i)
					}
					offset := int(binary.LittleEndian.Uint32(entry[4:]))
					if i == 2 {
						if offset != len(data) {
							t.Fatalf("BIF terminal offset %d != EOF %d", offset, len(data))
						}
						continue
					}
					next := int(binary.LittleEndian.Uint32(data[76+i*8 : 80+i*8]))
					if offset < indexEnd || next <= offset || next > len(data) || (i == 0 && offset != indexEnd) {
						t.Fatalf("invalid BIF frame %d bounds: %d..%d", i, offset, next)
					}
					if _, err := jpeg.DecodeConfig(bytes.NewReader(data[offset:next])); err != nil {
						t.Fatalf("BIF frame %d is not a JPEG: %v", i, err)
					}
				}
			})
		}
	}
}

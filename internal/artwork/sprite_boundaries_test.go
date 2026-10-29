package artwork

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpriteMultiPageCropAndBIFOffsets(t *testing.T) {
	db, m, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "100.5")
	green := filepath.Join(dir, "green.jpg")
	os.WriteFile(green, spritePicture(color.RGBA{10, 240, 10, 255}), 0600)
	opts.FFmpegPath = spriteScript(t, dir, "multi-ffmpeg", "case \" $* \" in *' 10.000 '*|*' 100.000 '*) exec /bin/cat '"+green+"';; *' 1.000 '*) exec /bin/cat '"+filepath.Join(dir, "blue.jpg")+"';; *) exec /bin/cat '"+filepath.Join(dir, "red.jpg")+"';; esac")
	res, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID, IntervalMs: 1000}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Count != 101 || res.Manifest.SheetCount != 2 || res.Manifest.Sheets[1].Count != 1 {
		t.Fatalf("%+v", res.Manifest)
	}
	for _, frame := range []int{1, 10, 100} {
		b, err := ReadSpriteFrame(context.Background(), db, m.ID, res.Manifest.Generation, frame, opts.RootDir)
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r, g, blue, _ := img.At(160, 90).RGBA()
		if (frame == 1 && blue <= g) || (frame != 1 && (g <= r || g <= blue)) {
			t.Fatalf("wrong tile for frame %d: %d/%d/%d", frame, r, g, blue)
		}
	}
	data, err := ReadSpriteBIF(context.Background(), db, m.ID, res.Manifest.Generation, opts.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(data[64+100*8:68+100*8]) != 100000 {
		t.Fatal("last BIF timestamp not 100000ms")
	}
}
func TestSpriteProbeAndRequestBoundaries(t *testing.T) {
	dir := t.TempDir()
	opts := spriteTestOptions(t, dir, "1")
	for _, value := range []string{"NaN", "+Inf", "0", "-1", "4294967.295", "garbage"} {
		opts.FFprobePath = spriteScript(t, dir, "probe-invalid", "printf '%s\\n' '"+value+"'")
		if _, err := spriteProbe(context.Background(), "https://trusted.example/video", opts); err == nil {
			t.Fatalf("accepted duration %q", value)
		}
	}
	db, m, dir := fixture(t)
	opts = spriteTestOptions(t, dir, "1")
	for _, interval := range []int64{-1, 4294967295} {
		if _, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID, IntervalMs: interval}, opts); err == nil {
			t.Fatalf("accepted interval %d", interval)
		}
	}
}
func TestSpriteProcessTimeoutAndOutputCap(t *testing.T) {
	for _, kind := range []string{"timeout", "output"} {
		t.Run(kind, func(t *testing.T) {
			db, m, dir := fixture(t)
			opts := spriteTestOptions(t, dir, "1")
			opts.Timeout = 100 * time.Millisecond
			if kind == "timeout" {
				opts.FFmpegPath = spriteScript(t, dir, "slow-ffmpeg", "sleep 20; exec /bin/cat '"+filepath.Join(dir, "red.jpg")+"'")
			} else {
				opts.Timeout = time.Second
				opts.FFmpegPath = spriteScript(t, dir, "noisy-ffmpeg", "exec yes oversized")
			}
			start := time.Now()
			res, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID}, opts)
			if err == nil || res.Changed || time.Since(start) > 3*time.Second {
				t.Fatalf("%+v %v duration=%v", res, err, time.Since(start))
			}
			entries, e := os.ReadDir(filepath.Join(opts.RootDir, fmt.Sprint(m.ID)))
			if e != nil || len(entries) != 0 {
				t.Fatalf("unpublished files leaked %v %v", entries, e)
			}
		})
	}
}
func TestSpriteStorageSymlinkAndUnknownCleanup(t *testing.T) {
	db, m, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "1")
	outside := t.TempDir()
	if err := os.Symlink(outside, opts.RootDir); err != nil {
		t.Skip(err)
	}
	if _, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID}, opts); err == nil {
		t.Fatal("followed storage root symlink")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside configured root")
	}
	if err := os.Remove(opts.RootDir); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(opts.RootDir, fmt.Sprint(m.ID), strings.Repeat("a", 32)), 0700)
	unknown := filepath.Join(opts.RootDir, fmt.Sprint(m.ID), strings.Repeat("a", 32), "unowned.txt")
	os.WriteFile(unknown, []byte("keep"), 0600)
	root, err := openSpriteRoot(opts.RootDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.root.Close()
	if err = removeSpriteGeneration(root, m.ID, strings.Repeat("a", 32)); err == nil {
		t.Fatal("deleted unknown generation content")
	}
	if b, e := os.ReadFile(unknown); e != nil || string(b) != "keep" {
		t.Fatal("unknown content removed")
	}
}
func TestSpriteRealFFmpegHTTP(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	video := filepath.Join(t.TempDir(), "clip.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x96:d=1.2", "-c:v", "mpeg4", "-movflags", "+faststart", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(video))))
	defer srv.Close()
	db, m, dir := fixture(t)
	if err = os.WriteFile(m.SourcePath, []byte(srv.URL+"/clip.mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := SpriteOptions{RootDir: filepath.Join(dir, "video-previews"), FFmpegPath: ff, FFprobePath: probe, Timeout: 45 * time.Second}
	res, err := GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID, IntervalMs: 500}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Count != 3 {
		t.Fatal(res.Manifest.Count)
	}
	if _, err = ReadSpriteBIF(ctx, db, m.ID, res.Manifest.Generation, opts.RootDir); err != nil {
		t.Fatal(err)
	}
	frame, err := ReadSpriteFrame(ctx, db, m.ID, res.Manifest.Generation, 2, opts.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(160, 90).RGBA()
	if b <= r || b <= g {
		t.Fatal("real frame not blue")
	}
}
func TestSpriteSourceChangedReader(t *testing.T) {
	db, m, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "1")
	res, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID}, opts)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(m.SourcePath, []byte("https://trusted.example/other"), 0600)
	if _, err = ReadSpriteBIF(context.Background(), db, m.ID, res.Manifest.Generation, opts.RootDir); !errors.Is(err, ErrSpriteChanged) {
		t.Fatal(err)
	}
}

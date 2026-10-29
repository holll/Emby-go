package artwork

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func spritePicture(c color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := range 180 {
		for x := range 320 {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}
func spriteScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func spriteTestOptions(t *testing.T, dir, duration string) SpriteOptions {
	t.Helper()
	red := filepath.Join(dir, "red.jpg")
	blue := filepath.Join(dir, "blue.jpg")
	os.WriteFile(red, spritePicture(color.RGBA{240, 10, 10, 255}), 0600)
	os.WriteFile(blue, spritePicture(color.RGBA{10, 10, 240, 255}), 0600)
	ff := spriteScript(t, dir, "sprite-ffmpeg", "case \" $* \" in *' 1.000 '*) exec /bin/cat '"+blue+"';; *) exec /bin/cat '"+red+"';; esac")
	probe := spriteScript(t, dir, "sprite-probe", "printf '%s\\n' '"+duration+"'")
	return SpriteOptions{RootDir: filepath.Join(dir, "video-previews"), FFmpegPath: ff, FFprobePath: probe, Timeout: 60 * time.Second}
}
func TestSpriteGenerateBIFSheetsMetadataAndRead(t *testing.T) {
	db, m, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "2.5")
	ctx := context.Background()
	poster := m.SourcePath + ".poster.jpg"
	nfo := strings.TrimSuffix(m.SourcePath, ".strm") + ".nfo"
	oldPic := picture()
	os.WriteFile(poster, oldPic, 0600)
	os.WriteFile(nfo, []byte("<movie>keep exact</movie>"), 0600)
	global, library := db.Version("g:version"), db.Version(fmt.Sprintf("lib:%d:version", m.LibraryID))
	progress := 0
	opts.Progress = func(done, total int) {
		if total != 3 || done != progress+1 {
			t.Errorf("progress %d/%d", done, total)
		}
		progress = done
	}
	res, err := GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID, IntervalMs: 1000}, opts)
	if err != nil || !res.Changed || res.Skipped {
		t.Fatalf("%+v %v", res, err)
	}
	manifest := res.Manifest
	if manifest.Count != 3 || manifest.DurationMs != 2500 || manifest.IntervalMs != 1000 || manifest.SheetCount != 1 || progress != 3 {
		t.Fatalf("%+v", manifest)
	}
	after, _ := db.Movie(m.ID)
	if !reflect.DeepEqual(m, after) || db.Version("g:version") != global || db.Version(fmt.Sprintf("lib:%d:version", m.LibraryID)) != library {
		t.Fatal("movie or index versions changed")
	}
	b, _ := os.ReadFile(poster)
	if !bytes.Equal(b, oldPic) {
		t.Fatal("poster changed")
	}
	b, _ = os.ReadFile(nfo)
	if string(b) != "<movie>keep exact</movie>" {
		t.Fatal("NFO changed")
	}
	got, err := GetSpriteManifest(ctx, db, m.ID, opts.RootDir)
	if err != nil || !reflect.DeepEqual(got, manifest) {
		t.Fatalf("%+v %v", got, err)
	}
	bif, err := ReadSpriteBIF(ctx, db, m.ID, manifest.Generation, opts.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateSpriteBIF(bif, manifest); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(bif[16:20]) != 1 || binary.LittleEndian.Uint32(bif[72:76]) != 1000 || binary.LittleEndian.Uint32(bif[88:92]) != ^uint32(0) {
		t.Fatal("nonstandard BIF index")
	}
	sheet, err := ReadSpriteSheet(ctx, db, m.ID, manifest.Generation, 0, opts.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = spriteDecode(sheet, 3200, 1800); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		frame, err := ReadSpriteFrame(ctx, db, m.ID, manifest.Generation, i, opts.RootDir)
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(frame))
		if err != nil {
			t.Fatal(err)
		}
		r, _, b, _ := img.At(160, 90).RGBA()
		if (i == 1 && b < r) || (i != 1 && r < b) {
			t.Fatalf("frame %d wrong tile r=%d b=%d", i, r, b)
		}
	}
	if _, err = ReadSpriteFrame(ctx, db, m.ID, manifest.Generation, 3, opts.RootDir); !errors.Is(err, ErrSpriteNotFound) {
		t.Fatal(err)
	}
	opts.FFmpegPath = "/nonexistent"
	opts.FFprobePath = "/nonexistent"
	res, err = GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID}, opts)
	if err != nil || !res.Skipped {
		t.Fatalf("skip %+v %v", res, err)
	}
}
func TestSpriteGenerationFailurePreservesOldAndAllowsReads(t *testing.T) {
	db, m, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "1")
	ctx := context.Background()
	res, err := GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID}, opts)
	if err != nil {
		t.Fatal(err)
	}
	old := res.Manifest.Generation
	for _, failure := range []string{"cancel", "source", "dimensions"} {
		t.Run(failure, func(t *testing.T) {
			work, cancel := context.WithCancel(ctx)
			defer cancel()
			current := opts
			seen := false
			if failure == "dimensions" {
				bad := filepath.Join(dir, "bad.jpg")
				os.WriteFile(bad, picture(), 0600)
				current.FFmpegPath = spriteScript(t, dir, "bad-ffmpeg", "exec /bin/cat '"+bad+"'")
			}
			current.Progress = func(done, total int) {
				seen = true
				readCtx, c := context.WithTimeout(ctx, time.Second)
				defer c()
				got, e := GetSpriteManifest(readCtx, db, m.ID, opts.RootDir)
				if e != nil || got.Generation != old {
					t.Errorf("old generation blocked during generate: %+v %v", got, e)
				}
				if failure == "cancel" {
					cancel()
				}
				if failure == "source" {
					os.WriteFile(m.SourcePath, []byte("https://trusted.example/changed"), 0600)
				}
			}
			out, e := GenerateSprite(work, db, SpriteRequest{MovieID: m.ID, Overwrite: true}, current)
			if e == nil || out.Changed {
				t.Fatalf("%+v %v", out, e)
			}
			if failure != "dimensions" && !seen {
				t.Fatal("did not reach frame")
			}
			state, _ := db.Sprite(m.ID)
			if state.Generation != old {
				t.Fatal("failed replacement changed pointer")
			}
			entries, e := os.ReadDir(filepath.Join(opts.RootDir, fmt.Sprint(m.ID)))
			if e != nil || len(entries) != 1 {
				t.Fatalf("temporary generation leaked: %v %v", entries, e)
			}
			if failure == "source" {
				if _, e = GetSpriteManifest(ctx, db, m.ID, opts.RootDir); !errors.Is(e, ErrSpriteChanged) {
					t.Fatalf("changed source served: %v", e)
				}
				os.WriteFile(m.SourcePath, []byte("https://trusted.example/video"), 0600)
			}
		})
	}
}
func TestSpriteRetentionAndSourceBoundaries(t *testing.T) {
	db, m, dir := fixture(t)
	opts := spriteTestOptions(t, dir, "0.5")
	ctx := context.Background()
	var generations []string
	for range 5 {
		res, err := GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID, Overwrite: true}, opts)
		if err != nil {
			t.Fatal(err)
		}
		generations = append(generations, res.Manifest.Generation)
	}
	entries, err := os.ReadDir(filepath.Join(opts.RootDir, fmt.Sprint(m.ID)))
	if err != nil || len(entries) != 3 {
		t.Fatalf("retention %d %v", len(entries), err)
	}
	if _, err = ReadSpriteBIF(ctx, db, m.ID, generations[3], opts.RootDir); err != nil {
		t.Fatal("retained generation unreadable", err)
	}
	if _, err = ReadSpriteBIF(ctx, db, m.ID, generations[0], opts.RootDir); !errors.Is(err, ErrSpriteNotFound) {
		t.Fatal(err)
	}
	for _, generation := range []string{"../escape", strings.Repeat("f", 32), strings.Repeat("a", 33)} {
		if _, err = ReadSpriteSheet(ctx, db, m.ID, generation, 0, opts.RootDir); err == nil {
			t.Fatalf("accepted %s", generation)
		}
	}
	active := generations[4]
	path := filepath.Join(opts.RootDir, fmt.Sprint(m.ID), active, "sheet-000.jpg")
	os.WriteFile(path, []byte("bad"), 0600)
	if _, err = ReadSpriteSheet(ctx, db, m.ID, active, 0, opts.RootDir); err == nil {
		t.Fatal("served corrupt sheet")
	}
}
func TestSpriteReadsDontCreateAndRejectInput(t *testing.T) {
	db, m, dir := fixture(t)
	ctx := context.Background()
	root := filepath.Join(dir, "nonexistent")
	if _, err := GetSpriteManifest(ctx, db, m.ID, root); !errors.Is(err, ErrSpriteNotFound) {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read created directory")
	}
	opts := spriteTestOptions(t, dir, "1")
	for _, value := range []string{"file:///etc/passwd", "http://user:pass@example.com/video", "https:///missing", "https://example.com/a\nhttps://example.com/b"} {
		os.WriteFile(m.SourcePath, []byte(value), 0600)
		if _, err := GenerateSprite(ctx, db, SpriteRequest{MovieID: m.ID}, opts); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
func TestSpriteBIFInvalidIndexAndManifestBounds(t *testing.T) {
	// Validate structural rejection without allowing a parser-controlled allocation.
	m := SpriteManifest{Count: 1, IntervalMs: 1000}
	data := make([]byte, 81)
	copy(data, spriteBIFMagic)
	binary.LittleEndian.PutUint32(data[12:16], 1)
	binary.LittleEndian.PutUint32(data[16:20], 1)
	binary.LittleEndian.PutUint32(data[68:72], 80)
	binary.LittleEndian.PutUint32(data[72:76], ^uint32(0))
	binary.LittleEndian.PutUint32(data[76:80], ^uint32(0))
	if err := validateSpriteBIF(data, m); err == nil {
		t.Fatal("accepted offset beyond EOF")
	}
}

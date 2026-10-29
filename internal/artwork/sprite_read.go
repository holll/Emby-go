package artwork

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"emby-go/internal/store"
)

func validateSpriteManifest(m spriteDiskManifest) error {
	if m.Version != 1 || !spriteHex(m.Generation, 32) || m.MovieID <= 0 || m.Width != spriteWidth || m.Height != spriteHeight || m.Cols != spriteCols || m.Rows != spriteRows || m.Count < 1 || m.Count > spriteMaxFrames || m.IntervalMs <= 0 || m.IntervalMs >= math.MaxUint32 || m.DurationMs <= 0 || m.DurationMs >= math.MaxUint32 || int64(m.Count) != (m.DurationMs+m.IntervalMs-1)/m.IntervalMs || m.SheetCount != (m.Count+99)/100 || len(m.Sheets) != m.SheetCount {
		return errors.New("invalid sprite manifest")
	}
	if m.State.Generation != m.Generation || m.State.Revision <= 0 || !spriteHex(m.State.SourceHash, 64) || m.BIFBytes < 64+int64(m.Count+1)*8 || m.BIFBytes > spriteMaxBytes || !spriteHex(m.BIFHash, 64) {
		return errors.New("invalid sprite identity or BIF size")
	}
	total := m.BIFBytes
	for page, s := range m.Sheets {
		count := min(m.Count-page*100, 100)
		if s.File != fmt.Sprintf("sheet-%03d.jpg", page) || s.Count != count || s.Width != spriteWidth*spriteCols || s.Height != spriteHeight*spriteRows || s.Bytes <= 0 || s.Bytes > spriteMaxSheetBytes || !spriteHex(s.SHA256, 64) {
			return errors.New("invalid sprite sheet record")
		}
		total += s.Bytes
	}
	if total > spriteMaxBytes {
		return errors.New("sprite generation exceeds byte limit")
	}
	return nil
}
func readSpriteDisk(r *spriteRoot, id int64, generation string) (spriteDiskManifest, error) {
	var m spriteDiskManifest
	if id <= 0 || !spriteHex(generation, 32) {
		return m, ErrSpriteNotFound
	}
	path := filepath.Join(spriteGenerationDir(id, generation), "manifest.json")
	b, err := readLimited(r.root, path, spriteMaxManifestBytes)
	if os.IsNotExist(err) {
		return m, ErrSpriteNotFound
	}
	if err != nil {
		return m, err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&m); err != nil {
		return m, err
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return m, errors.New("trailing sprite manifest data")
	}
	if m.Generation != generation || m.MovieID != id {
		return m, ErrSpriteChanged
	}
	if err = validateSpriteManifest(m); err != nil {
		return m, err
	}
	return m, nil
}
func readSpriteAsset(ctx context.Context, r *spriteRoot, m spriteDiskManifest, name string, size int64, hash string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.verify(); err != nil {
		return nil, err
	}
	b, err := readLimited(r.root, filepath.Join(spriteGenerationDir(m.MovieID, m.Generation), name), limit)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != size || spriteSum(b) != hash {
		return nil, ErrSpriteChanged
	}
	return b, ctx.Err()
}
func validateSpriteFiles(ctx context.Context, r *spriteRoot, m spriteDiskManifest) error {
	if err := validateSpriteManifest(m); err != nil {
		return err
	}
	for _, s := range m.Sheets {
		b, err := readSpriteAsset(ctx, r, m, s.File, s.Bytes, s.SHA256, spriteMaxSheetBytes)
		if err != nil {
			return err
		}
		if _, err = spriteDecode(b, s.Width, s.Height); err != nil {
			return err
		}
	}
	b, err := readSpriteAsset(ctx, r, m, "index.bif", m.BIFBytes, m.BIFHash, spriteMaxBytes)
	if err != nil {
		return err
	}
	return validateSpriteBIF(b, m.SpriteManifest)
}

type spriteReadHandle struct {
	source   *spriteSource
	root     *spriteRoot
	manifest spriteDiskManifest
	unlock   func()
}

func (h *spriteReadHandle) close() { h.source.root.Close(); h.root.root.Close(); h.unlock() }
func openSpriteRead(ctx context.Context, db *store.Store, id int64, generation, rootDir string) (*spriteReadHandle, error) {
	if id <= 0 || generation != "" && !spriteHex(generation, 32) {
		return nil, ErrSpriteNotFound
	}
	unlock, err := spriteLock(ctx, id)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			unlock()
		}
	}()
	source, err := openSpriteSource(ctx, db, id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			source.root.Close()
		}
	}()
	current, err := db.SpriteContext(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSpriteNotFound
	}
	if err != nil {
		return nil, err
	}
	if !sameSpriteSource(current, source.state("", 0)) {
		return nil, ErrSpriteChanged
	}
	if generation == "" {
		generation = current.Generation
	}
	r, err := openSpriteRoot(rootDir, false)
	if os.IsNotExist(err) {
		return nil, ErrSpriteNotFound
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			r.root.Close()
		}
	}()
	m, err := readSpriteDisk(r, id, generation)
	if err != nil {
		return nil, err
	}
	if !sameSpriteSource(m.State, current) || m.State.Revision > current.Revision {
		return nil, ErrSpriteChanged
	}
	if generation == current.Generation && m.State != current {
		return nil, ErrSpriteChanged
	}
	if err = source.verify(ctx); err != nil {
		return nil, err
	}
	if err = r.verify(); err != nil {
		return nil, err
	}
	ok = true
	return &spriteReadHandle{source: source, root: r, manifest: m, unlock: unlock}, nil
}
func (h *spriteReadHandle) verify(ctx context.Context, db *store.Store) error {
	if err := h.source.verify(ctx); err != nil {
		return err
	}
	if err := h.root.verify(); err != nil {
		return err
	}
	movie, libraryRoot, err := db.SpriteSource(ctx, h.manifest.MovieID)
	if err != nil {
		return err
	}
	if movie.LibraryID != h.source.movie.LibraryID || movie.SourcePath != h.source.movie.SourcePath || libraryRoot != h.source.libraryRoot {
		return ErrSpriteChanged
	}
	return ctx.Err()
}

// GetSpriteManifest and asset reads never create directories or start FFmpeg.
// Retained generations can be read only while they match the live source binding.
func GetSpriteManifest(ctx context.Context, db *store.Store, movieID int64, rootDir string) (SpriteManifest, error) {
	h, err := openSpriteRead(ctx, db, movieID, "", rootDir)
	if err != nil {
		return SpriteManifest{}, err
	}
	defer h.close()
	if err = h.verify(ctx, db); err != nil {
		return SpriteManifest{}, err
	}
	return h.manifest.SpriteManifest, nil
}
func ReadSpriteSheet(ctx context.Context, db *store.Store, movieID int64, generation string, page int, rootDir string) ([]byte, error) {
	if page < 0 || page >= 6 {
		return nil, ErrSpriteNotFound
	}
	h, err := openSpriteRead(ctx, db, movieID, generation, rootDir)
	if err != nil {
		return nil, err
	}
	defer h.close()
	return h.sheet(ctx, db, page)
}
func (h *spriteReadHandle) sheet(ctx context.Context, db *store.Store, page int) ([]byte, error) {
	if page < 0 || page >= len(h.manifest.Sheets) {
		return nil, ErrSpriteNotFound
	}
	s := h.manifest.Sheets[page]
	b, err := readSpriteAsset(ctx, h.root, h.manifest, s.File, s.Bytes, s.SHA256, spriteMaxSheetBytes)
	if err != nil {
		return nil, err
	}
	if _, err = spriteDecode(b, s.Width, s.Height); err != nil {
		return nil, err
	}
	if err = h.verify(ctx, db); err != nil {
		return nil, err
	}
	return b, nil
}
func ReadSpriteBIF(ctx context.Context, db *store.Store, movieID int64, generation, rootDir string) ([]byte, error) {
	h, err := openSpriteRead(ctx, db, movieID, generation, rootDir)
	if err != nil {
		return nil, err
	}
	defer h.close()
	m := h.manifest
	b, err := readSpriteAsset(ctx, h.root, m, "index.bif", m.BIFBytes, m.BIFHash, spriteMaxBytes)
	if err != nil {
		return nil, err
	}
	if err = validateSpriteBIF(b, m.SpriteManifest); err != nil {
		return nil, err
	}
	if err = h.verify(ctx, db); err != nil {
		return nil, err
	}
	return b, nil
}
func ReadSpriteFrame(ctx context.Context, db *store.Store, movieID int64, generation string, frame int, rootDir string) ([]byte, error) {
	if frame < 0 || frame >= spriteMaxFrames {
		return nil, ErrSpriteNotFound
	}
	h, err := openSpriteRead(ctx, db, movieID, generation, rootDir)
	if err != nil {
		return nil, err
	}
	defer h.close()
	if frame >= h.manifest.Count {
		return nil, ErrSpriteNotFound
	}
	b, err := h.sheet(ctx, db, frame/100)
	if err != nil {
		return nil, err
	}
	sheet, err := spriteDecode(b, spriteWidth*spriteCols, spriteHeight*spriteRows)
	if err != nil {
		return nil, err
	}
	cell := frame % 100
	x, y := (cell%spriteCols)*spriteWidth, (cell/spriteCols)*spriteHeight
	img := image.NewRGBA(image.Rect(0, 0, spriteWidth, spriteHeight))
	draw.Draw(img, img.Bounds(), sheet, image.Pt(x, y), draw.Src)
	var out bytes.Buffer
	if err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	if out.Len() > spriteMaxFrameBytes {
		return nil, errors.New("sprite frame exceeds byte limit")
	}
	if err = h.verify(ctx, db); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type spriteRetained struct {
	generation string
	created    int64
}

func pruneSpriteGenerations(ctx context.Context, r *spriteRoot, id int64, active string, revision int64, keep int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.verify(); err != nil {
		return err
	}
	dir := spriteMovieDir(id)
	if err := safe(r.root, dir); err != nil {
		return err
	}
	before, err := r.root.Lstat(dir)
	if err != nil {
		return err
	}
	if !before.IsDir() {
		return errors.New("sprite movie path is not a directory")
	}
	root, err := r.root.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return ErrSpriteChanged
	}
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(65)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > 64 {
		return errors.New("sprite directory quota exceeded")
	}
	var retained []spriteRetained
	for _, entry := range entries {
		if !spriteHex(entry.Name(), 32) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unrecognized sprite directory content")
		}
		if entry.Name() == active {
			continue
		}
		info, e := root.Lstat(entry.Name())
		if e != nil {
			return e
		}
		m, e := readSpriteDisk(r, id, entry.Name())
		if e == nil && m.State.Revision >= revision {
			if time.Since(info.ModTime()) < 17*time.Minute {
				return errors.New("another sprite generation is in progress")
			}
			if e = removeSpriteGeneration(r, id, entry.Name(), info); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			// A crashed unpublished generation has no manifest. Only the fixed set of
			// regular output names can be removed; unknown content is never recursive.
			if !errors.Is(e, ErrSpriteNotFound) {
				return e
			}
			if time.Since(info.ModTime()) < 17*time.Minute {
				return errors.New("another sprite generation is in progress")
			}
			if err = removeSpriteGeneration(r, id, entry.Name(), info); err != nil {
				return err
			}
			continue
		}
		retained = append(retained, spriteRetained{entry.Name(), m.Created})
	}
	sort.Slice(retained, func(i, j int) bool { return retained[i].created > retained[j].created })
	for i := keep; i < len(retained); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err = removeSpriteGeneration(r, id, retained[i].generation); err != nil {
			return err
		}
	}
	return nil
}

// Deliberately not RemoveAll: reject links, nested directories and unknown
// names. All removal is anchored to a validated generation's open directory.
func removeSpriteGeneration(r *spriteRoot, id int64, generation string, expected ...os.FileInfo) error {
	if id <= 0 || !spriteHex(generation, 32) {
		return errors.New("invalid sprite cleanup target")
	}
	// An owned generation is cleaned through the original Root handle even if
	// its configured pathname was moved. Its inode must still match expected.
	if len(expected) == 0 {
		if err := r.verify(); err != nil {
			return err
		}
	}
	return removeSpriteDirectory(r.root, spriteGenerationDir(id, generation), expected...)
}

// parent is an already anchored namespace. In particular, failed generation
// cleanup must use the original movieRoot, even if that directory was renamed.
func removeSpriteDirectory(parent *os.Root, dir string, expected ...os.FileInfo) error {
	if err := safe(parent, dir); err != nil {
		return err
	}
	before, err := parent.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !before.IsDir() {
		return errors.New("invalid sprite cleanup directory")
	}
	if len(expected) > 0 && !os.SameFile(expected[0], before) {
		return ErrSpriteChanged
	}
	root, err := parent.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return ErrSpriteChanged
	}
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(10)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > 8 {
		return errors.New("sprite cleanup file quota exceeded")
	}
	for _, entry := range entries {
		name := entry.Name()
		valid := name == "manifest.json" || name == "index.bif"
		for i := range 6 {
			valid = valid || name == fmt.Sprintf("sheet-%03d.jpg", i)
		}
		info, e := root.Lstat(name)
		if e != nil {
			return e
		}
		if !valid || !info.Mode().IsRegular() {
			return errors.New("unrecognized sprite cleanup file")
		}
	}
	for _, entry := range entries {
		if err = root.Remove(entry.Name()); err != nil {
			return err
		}
	}
	now, err := parent.Lstat(dir)
	if err != nil {
		return err
	}
	if !os.SameFile(before, now) {
		return ErrSpriteChanged
	}
	return parent.Remove(dir)
}

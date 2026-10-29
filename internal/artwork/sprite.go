package artwork

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"emby-go/internal/store"
)

const (
	spriteWidth            = 320
	spriteHeight           = 180
	spriteCols             = 10
	spriteRows             = 10
	spriteMaxFrames        = 600
	spriteMaxFrameBytes    = 256 << 10
	spriteMaxSheetBytes    = 8 << 20
	spriteMaxBytes         = 64 << 20
	spriteMaxManifestBytes = 1 << 20
)

var ErrSpriteChanged = store.ErrSpriteChanged
var ErrSpriteNotFound = errors.New("sprite not found")
var spriteRunGate = make(chan struct{}, 1)

// Reads and generation also share the per-movie lock: pruning cannot delete an
// old generation while a response is being read. Striping bounds lock memory.
var spriteLocks [64]sync.Mutex

type SpriteRequest struct {
	MovieID    int64
	IntervalMs int64
	Overwrite  bool
}
type SpriteOptions struct {
	RootDir, FFmpegPath, FFprobePath string
	Timeout                          time.Duration
	Progress                         func(done, total int)
}
type SpriteSheet struct {
	File   string `json:"file"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Count  int    `json:"count"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type SpriteManifest struct {
	Version    int           `json:"version"`
	Generation string        `json:"generation"`
	MovieID    int64         `json:"movie_id"`
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	Cols       int           `json:"cols"`
	Rows       int           `json:"rows"`
	Count      int           `json:"count"`
	IntervalMs int64         `json:"interval_ms"`
	DurationMs int64         `json:"duration_ms"`
	SheetCount int           `json:"sheet_count"`
	Sheets     []SpriteSheet `json:"sheets"`
}
type SpriteResult struct {
	MovieID  int64          `json:"movie_id"`
	Changed  bool           `json:"changed"`
	Skipped  bool           `json:"skipped"`
	Manifest SpriteManifest `json:"manifest"`
}
type spriteDiskManifest struct {
	SpriteManifest
	State    store.SpriteState `json:"source"`
	Created  int64             `json:"created"`
	BIFBytes int64             `json:"bif_bytes"`
	BIFHash  string            `json:"bif_sha256"`
}

func spriteHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func spriteSum(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func trustedStreamURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return errors.New("STRM must contain one HTTP(S) URL without credentials")
	}
	return nil
}

type spriteSource struct {
	movie                                store.Movie
	libraryRoot, rootPath, relative, raw string
	root                                 *os.Root
	rootInfo                             os.FileInfo
	original                             stamp
}

func openSpriteSource(ctx context.Context, db *store.Store, id int64) (*spriteSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if db == nil || id <= 0 {
		return nil, errors.New("store and valid movie ID required")
	}
	movie, libraryRoot, err := db.SpriteSource(ctx, id)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(libraryRoot)
	if err != nil {
		return nil, err
	}
	if err = noSymlinks(abs); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	s := &spriteSource{movie: movie, libraryRoot: libraryRoot, rootPath: abs, root: root}
	ok := false
	defer func() {
		if !ok {
			root.Close()
		}
	}()
	s.rootInfo, err = root.Stat(".")
	if err != nil {
		return nil, err
	}
	s.relative, err = relative(abs, movie.SourcePath)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(s.relative), ".strm") {
		return nil, errors.New("indexed source is not STRM")
	}
	s.original, err = fingerprint(root, s.relative)
	if err != nil {
		return nil, err
	}
	if !s.original.Exists {
		return nil, ErrSpriteNotFound
	}
	raw, err := readLimited(root, s.relative, 64<<10)
	if err != nil {
		return nil, err
	}
	s.raw = strings.TrimSpace(string(raw))
	if err = trustedStreamURL(s.raw); err != nil {
		return nil, err
	}
	if err = s.verify(ctx); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}
func (s *spriteSource) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := noSymlinks(s.rootPath); err != nil {
		return err
	}
	info, err := os.Stat(s.rootPath)
	if err != nil {
		return err
	}
	if !os.SameFile(s.rootInfo, info) {
		return ErrSpriteChanged
	}
	now, err := fingerprint(s.root, s.relative)
	if err != nil {
		return err
	}
	if !equal(s.original, now) {
		return ErrSpriteChanged
	}
	return ctx.Err()
}
func (s *spriteSource) state(generation string, revision int64) store.SpriteState {
	return store.SpriteState{Revision: revision, Generation: generation, LibraryID: s.movie.LibraryID, SourcePath: s.movie.SourcePath, LibraryRoot: s.libraryRoot, SourceHash: hex.EncodeToString(s.original.Hash[:])}
}
func sameSpriteSource(a, b store.SpriteState) bool {
	return a.LibraryID == b.LibraryID && a.LibraryRoot == b.LibraryRoot && a.SourcePath == b.SourcePath && a.SourceHash == b.SourceHash
}

type spriteRoot struct {
	root *os.Root
	path string
	info os.FileInfo
}

func openSpriteRoot(path string, create bool) (*spriteRoot, error) {
	if path == "" {
		return nil, errors.New("sprite root required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// RootDir comes only from trusted server configuration. Anchor its existing
	// parent before creating the single child; do not mkdir arbitrary ancestors.
	parent := filepath.Dir(abs)
	if err = noSymlinks(parent); err != nil {
		return nil, err
	}
	p, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	name := filepath.Base(abs)
	if name == "." || name == string(filepath.Separator) {
		return nil, errors.New("invalid sprite root")
	}
	if create {
		if err = p.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	if err = safe(p, name); err != nil {
		return nil, err
	}
	before, err := p.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, errors.New("sprite root is not a directory")
	}
	root, err := p.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil || !os.SameFile(before, info) {
		root.Close()
		return nil, ErrSpriteChanged
	}
	r := &spriteRoot{root: root, path: abs, info: info}
	if err = r.verify(); err != nil {
		root.Close()
		return nil, err
	}
	return r, nil
}
func (r *spriteRoot) verify() error {
	if err := noSymlinks(r.path); err != nil {
		return err
	}
	now, err := os.Stat(r.path)
	if err != nil {
		return err
	}
	if !os.SameFile(r.info, now) {
		return ErrSpriteChanged
	}
	return nil
}
func spriteMovieDir(id int64) string { return strconv.FormatInt(id, 10) }
func spriteGenerationDir(id int64, generation string) string {
	return filepath.Join(spriteMovieDir(id), generation)
}

func spriteLock(ctx context.Context, id int64) (func(), error) {
	lock := &spriteLocks[uint64(id)%uint64(len(spriteLocks))]
	for !lock.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		lock.Unlock()
		return nil, err
	}
	return lock.Unlock, nil
}

func spriteProbe(ctx context.Context, raw string, opts SpriteOptions) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe := opts.FFprobePath
	if probe == "" {
		probe = "ffprobe"
	}
	b, err := command(ctx, probe, []string{"-v", "error", "-protocol_whitelist", protocols, "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", raw}, 4096)
	if err != nil {
		return 0, err
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	// BIF timestamps and offsets are uint32; cap the whole duration accordingly.
	if err != nil || !finite(seconds) || seconds <= 0 || seconds > float64(math.MaxUint32-1)/1000 {
		return 0, errors.New("stream duration unavailable or outside BIF timestamp range")
	}
	ms := int64(math.Ceil(seconds * 1000))
	if ms <= 0 || ms >= math.MaxUint32 {
		return 0, errors.New("invalid sprite duration")
	}
	return ms, nil
}
func spriteFrame(ctx context.Context, raw string, ms int64, opts SpriteOptions) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ffmpeg := opts.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	args := []string{"-nostdin", "-v", "error", "-protocol_whitelist", protocols, "-max_pixels", strconv.Itoa(maxPixels), "-threads", "1", "-ss", strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64), "-i", raw, "-map", "0:v:0", "-frames:v", "1", "-vf", "scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2", "-threads", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1"}
	b, err := command(ctx, ffmpeg, args, spriteMaxFrameBytes)
	if err != nil {
		return nil, err
	}
	if _, err = spriteDecode(b, spriteWidth, spriteHeight); err != nil {
		return nil, err
	}
	return b, nil
}
func spriteDecode(b []byte, w, h int) (image.Image, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "jpeg" || cfg.Width != w || cfg.Height != h || int64(w)*int64(h) > maxPixels {
		return nil, errors.New("invalid sprite JPEG dimensions")
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("invalid sprite JPEG")
	}
	return img, nil
}
func spriteWrite(root *os.Root, path string, b []byte) error {
	if err := safe(root, filepath.Dir(path)); err != nil {
		return err
	}
	f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

// GenerateSprite samples only the indexed trusted STRM. The sole publication
// point is the database sprite pointer, after every immutable file is complete.
func GenerateSprite(ctx context.Context, db *store.Store, req SpriteRequest, opts SpriteOptions) (res SpriteResult, err error) {
	res.MovieID = req.MovieID
	if req.MovieID <= 0 || req.IntervalMs < 0 || req.IntervalMs > math.MaxUint32-1 {
		return res, errors.New("invalid sprite request")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if timeout > 15*time.Minute {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case spriteRunGate <- struct{}{}:
		defer func() { <-spriteRunGate }()
	case <-ctx.Done():
		return res, ctx.Err()
	}
	unlock, err := spriteLock(ctx, req.MovieID)
	if err != nil {
		return res, err
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	source, err := openSpriteSource(ctx, db, req.MovieID)
	if err != nil {
		return res, err
	}
	defer source.root.Close()
	old, err := db.SpriteContext(ctx, req.MovieID)
	if errors.Is(err, sql.ErrNoRows) {
		old = store.SpriteState{}
	} else if err != nil {
		return res, err
	}
	if !req.Overwrite && old.Revision > 0 && sameSpriteSource(old, source.state("", 0)) {
		r, e := openSpriteRoot(opts.RootDir, false)
		if e == nil {
			defer r.root.Close()
			m, e := readSpriteDisk(r, req.MovieID, old.Generation)
			if e == nil && m.State == old {
				if e = validateSpriteFiles(ctx, r, m); e == nil {
					res.Skipped = true
					res.Manifest = m.SpriteManifest
					return res, nil
				}
			}
		}
		if e := ctx.Err(); e != nil {
			return res, e
		}
		// Missing or corrupt generated data is not silently replaced without an
		// explicit overwrite, just like an existing independent poster.
		return res, errors.New("existing sprite is invalid; explicit overwrite required")
	}
	r, err := openSpriteRoot(opts.RootDir, true)
	if err != nil {
		return res, err
	}
	defer r.root.Close()
	movieDir := spriteMovieDir(req.MovieID)
	if err = r.root.Mkdir(movieDir, 0700); err != nil && !os.IsExist(err) {
		return res, err
	}
	if err = safe(r.root, movieDir); err != nil {
		return res, err
	}
	movieInfo, err := r.root.Lstat(movieDir)
	if err != nil {
		return res, err
	}
	if !movieInfo.IsDir() {
		return res, errors.New("sprite movie path is not a directory")
	}
	movieRoot, err := r.root.OpenRoot(movieDir)
	if err != nil {
		return res, err
	}
	defer movieRoot.Close()
	openedMovie, err := movieRoot.Stat(".")
	if err != nil || !os.SameFile(movieInfo, openedMovie) {
		return res, ErrSpriteChanged
	}
	// Before generation, prune only validated non-active directories. Unknown or
	// excessive disk content blocks the task rather than being followed/deleted.
	if err = pruneSpriteGenerations(ctx, r, req.MovieID, old.Generation, old.Revision, 2); err != nil {
		return res, err
	}
	unlock()
	locked = false
	duration, err := spriteProbe(ctx, source.raw, opts)
	if err != nil {
		return res, err
	}
	interval := req.IntervalMs
	if interval == 0 {
		interval = 10000
	}
	minInterval := (duration + spriteMaxFrames - 1) / spriteMaxFrames
	if interval < minInterval {
		interval = minInterval
	}
	count := int((duration + interval - 1) / interval)
	if count < 1 || count > spriteMaxFrames {
		return res, errors.New("invalid sprite frame count")
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return res, err
	}
	generation := hex.EncodeToString(random[:])
	dir := spriteGenerationDir(req.MovieID, generation)
	if err = movieRoot.Mkdir(generation, 0700); err != nil {
		return res, err
	}
	genInfo, err := movieRoot.Lstat(generation)
	if err != nil {
		return res, err
	}
	genRoot, err := movieRoot.OpenRoot(generation)
	if err != nil {
		return res, err
	}
	defer genRoot.Close()
	openedGen, err := genRoot.Stat(".")
	if err != nil || !os.SameFile(genInfo, openedGen) {
		return res, ErrSpriteChanged
	}
	verifyGeneration := func() error {
		if e := r.verify(); e != nil {
			return e
		}
		if e := safe(r.root, dir); e != nil {
			return e
		}
		parent, e := r.root.Lstat(movieDir)
		if e != nil {
			return e
		}
		if !os.SameFile(movieInfo, parent) {
			return ErrSpriteChanged
		}
		now, e := r.root.Lstat(dir)
		if e != nil {
			return e
		}
		if !os.SameFile(genInfo, now) {
			return ErrSpriteChanged
		}
		return nil
	}
	published := false
	defer func() {
		if !published {
			_ = removeSpriteDirectory(movieRoot, generation, genInfo)
		}
	}()
	m := spriteDiskManifest{Version: 1, Generation: generation, MovieID: req.MovieID, Width: spriteWidth, Height: spriteHeight, Cols: spriteCols, Rows: spriteRows, Count: count, IntervalMs: interval, DurationMs: duration, SheetCount: (count + 99) / 100, State: source.state(generation, old.Revision+1), Created: time.Now().UnixNano()}
	bif, err := newSpriteBIF(genRoot, "index.bif", count)
	if err != nil {
		return res, err
	}
	defer bif.close()
	totalBytes := int64(0)
	done := 0
	for page := 0; page < m.SheetCount; page++ {
		if err = ctx.Err(); err != nil {
			return res, err
		}
		sheet := image.NewRGBA(image.Rect(0, 0, spriteWidth*spriteCols, spriteHeight*spriteRows))
		pageCount := min(count-done, 100)
		for cell := range pageCount {
			if err = source.verify(ctx); err != nil {
				return res, err
			}
			frame, e := spriteFrame(ctx, source.raw, int64(done)*interval, opts)
			if e != nil {
				return res, e
			}
			img, e := spriteDecode(frame, spriteWidth, spriteHeight)
			if e != nil {
				return res, e
			}
			x, y := (cell%spriteCols)*spriteWidth, (cell/spriteCols)*spriteHeight
			draw.Draw(sheet, image.Rect(x, y, x+spriteWidth, y+spriteHeight), img, img.Bounds().Min, draw.Src)
			if totalBytes+bif.size+int64(len(frame)) > spriteMaxBytes {
				return res, errors.New("sprite generation exceeds byte limit")
			}
			if err = bif.frame(uint32(int64(done)*interval), frame); err != nil {
				return res, err
			}
			done++
			if opts.Progress != nil {
				opts.Progress(done, count)
			}
		}
		var encoded bytes.Buffer
		if err = jpeg.Encode(&encoded, sheet, &jpeg.Options{Quality: 85}); err != nil {
			return res, err
		}
		if encoded.Len() > spriteMaxSheetBytes {
			return res, errors.New("sprite sheet exceeds byte limit")
		}
		b := encoded.Bytes()
		totalBytes += int64(len(b))
		if totalBytes+bif.size > spriteMaxBytes {
			return res, errors.New("sprite generation exceeds byte limit")
		}
		name := fmt.Sprintf("sheet-%03d.jpg", page)
		if err = spriteWrite(genRoot, name, b); err != nil {
			return res, err
		}
		m.Sheets = append(m.Sheets, SpriteSheet{File: name, Width: spriteWidth * spriteCols, Height: spriteHeight * spriteRows, Count: pageCount, Bytes: int64(len(b)), SHA256: spriteSum(b)})
	}
	if err = bif.finish(); err != nil {
		return res, err
	}
	m.BIFBytes = bif.size
	m.BIFHash = bif.hash
	raw, err := json.Marshal(m)
	if err != nil {
		return res, err
	}
	if len(raw) > spriteMaxManifestBytes || totalBytes+m.BIFBytes+int64(len(raw)) > spriteMaxBytes {
		return res, errors.New("sprite generation exceeds byte limit")
	}
	if err = spriteWrite(genRoot, "manifest.json", raw); err != nil {
		return res, err
	}
	if err = syncSpriteDir(genRoot, "."); err != nil {
		return res, err
	}
	unlock, err = spriteLock(ctx, req.MovieID)
	if err != nil {
		return res, err
	}
	locked = true
	// verify re-reads the source, the generation directory and every sheet plus
	// the BIF index (tens of megabytes). It is the store's Prepare phase, so it
	// runs before writeMu and the SQLite write transaction are taken; leaving it
	// inside the transaction pins both locks for the whole read (P1-4). The
	// generation itself is already written and synced above, so the committed
	// pointer UPDATE performed by the store is the only publish step left.
	verify := func() error {
		if e := source.verify(ctx); e != nil {
			return e
		}
		if e := verifyGeneration(); e != nil {
			return e
		}
		if e := validateSpriteFiles(ctx, r, m); e != nil {
			return e
		}
		if phaseHook != nil {
			phaseHook("prepare-done")
		}
		return nil
	}
	err = db.CompareAndSwapSpriteFiles(ctx, source.movie, source.libraryRoot, old, m.State, store.SpriteFiles{Prepare: verify})
	if err != nil {
		return res, err
	}
	published = true
	res.Changed = true
	res.Manifest = m.SpriteManifest
	// Keep active plus at most two older generations; inability to remove an old
	// directory never rolls back an already committed preview. The next task will
	// refuse quota-exceeding or unrecognized content before allocating another.
	_ = pruneSpriteGenerations(ctx, r, req.MovieID, generation, m.State.Revision, 2)
	return res, nil
}

func syncSpriteDir(root *os.Root, path string) error {
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Package artwork performs image-only changes. FFmpeg's protocol whitelist blocks
// local files and concat/crypto, NOT private HTTP destinations or redirects:
// screenshot URLs must come from trusted library STRM files, not untrusted input.
package artwork

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
	_ "github.com/deepteams/webp"
)

type Request struct {
	MovieID         int64
	Mode            string
	Provider        string
	ProviderID      string
	Overwrite       bool
	PositionSeconds *float64
	PositionPercent *float64
}
type Options struct {
	FFmpegPath  string
	FFprobePath string
	Timeout     time.Duration
	Client      *metatube.Client
}
type Result struct {
	MovieID      int64
	Changed      bool
	Skipped      bool
	Reason       string
	RefreshError string
	PosterPath   string
}

const maxImageBytes = 20 << 20
const maxPixels = 20_000_000
const protocols = "http,https,tcp,tls"

// Keep positions representable as a Go duration and comfortably within ffmpeg's
// signed microsecond timestamp range. Avoid duration*pct overflowing first.
const maxSeekSeconds = float64(math.MaxInt64) / float64(time.Second)

func validate(req Request) error {
	if req.MovieID <= 0 {
		return errors.New("invalid movie ID")
	}
	switch req.Mode {
	case "screenshot":
		if req.Provider != "" || req.ProviderID != "" {
			return errors.New("screenshot mode does not accept provider")
		}
		if req.PositionSeconds != nil && req.PositionPercent != nil {
			return errors.New("choose seconds or percent, not both")
		}
		if req.PositionSeconds != nil && (!finite(*req.PositionSeconds) || *req.PositionSeconds < 0 || *req.PositionSeconds > maxSeekSeconds) {
			return errors.New("invalid screenshot seconds")
		}
		if req.PositionPercent != nil && (!finite(*req.PositionPercent) || *req.PositionPercent < 0 || *req.PositionPercent >= 100) {
			return errors.New("invalid screenshot percent")
		}
	case "provider":
		if !safeToken(req.Provider) || !safeToken(req.ProviderID) {
			return errors.New("provider and provider ID required")
		}
		if req.PositionSeconds != nil || req.PositionPercent != nil {
			return errors.New("provider mode does not accept screenshot position")
		}
	default:
		return errors.New("unsupported artwork mode")
	}
	return nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func safeToken(s string) bool {
	if s == "" || len(s) > 256 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("/\\?&#%", r) {
			return false
		}
	}
	return strings.TrimSpace(s) == s
}

// Run reads only the indexed source and changes only its independent poster.
func Run(ctx context.Context, db *store.Store, req Request, opts Options) (res Result, err error) {
	res.MovieID = req.MovieID
	if err = validate(req); err != nil {
		return
	}
	if db == nil {
		return res, errors.New("store required")
	}
	timeout := opts.Timeout
	if timeout <= 0 || timeout > 90*time.Second {
		timeout = 90 * time.Second
	}
	workCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Include validation, local I/O and waiting for the index lock.
	ctx = workCtx
	if err = ctx.Err(); err != nil {
		return
	}
	movie, err := db.Movie(req.MovieID)
	if err != nil {
		return res, err
	}
	lib, err := db.Library(movie.LibraryID)
	if err != nil {
		return res, err
	}
	rootPath, err := filepath.Abs(lib.Path)
	if err != nil {
		return res, err
	}
	// Reject linked library ancestors as well as linked files inside the root.
	if err = noSymlinks(rootPath); err != nil {
		return res, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return res, err
	}
	defer root.Close()
	source, err := relative(rootPath, movie.SourcePath)
	if err != nil {
		return res, err
	}
	if !strings.EqualFold(filepath.Ext(source), ".strm") {
		return res, errors.New("indexed source is not STRM")
	}
	if err = safe(root, source); err != nil {
		return res, err
	}
	// Anchor the source directory. Renaming a parent or swapping it for an
	// in-library symlink must never redirect the final installation elsewhere.
	sourceDir := filepath.Dir(source)
	parentInfo, err := root.Lstat(sourceDir)
	if err != nil {
		return res, err
	}
	files, err := root.OpenRoot(sourceDir)
	if err != nil {
		return res, err
	}
	defer files.Close()
	openedParent, err := files.Stat(".")
	if err != nil {
		return res, err
	}
	if !os.SameFile(parentInfo, openedParent) {
		return res, store.ErrArtworkChanged
	}
	sourceFile := filepath.Base(source)
	targetFile := sourceFile + ".poster.jpg"
	original, err := fingerprint(files, sourceFile)
	if err != nil {
		return res, err
	}
	if !original.Exists {
		return res, errors.New("indexed STRM does not exist")
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return res, err
	}
	checkRoot := func() error {
		if e := noSymlinks(rootPath); e != nil {
			return e
		}
		current, e := os.Stat(rootPath)
		if e != nil {
			return e
		}
		if !os.SameFile(rootInfo, current) {
			return store.ErrArtworkChanged
		}
		if e := safe(root, sourceDir); e != nil {
			return e
		}
		parent, e := root.Lstat(sourceDir)
		if e != nil {
			return e
		}
		if !os.SameFile(parentInfo, parent) {
			return store.ErrArtworkChanged
		}
		return nil
	}
	target := source + ".poster.jpg"
	res.PosterPath = filepath.Join(rootPath, target)
	before, err := fingerprint(files, targetFile)
	if err != nil {
		return res, err
	}
	var indexedPoster string
	var indexedBefore stamp
	if movie.PosterPath != "" {
		p, e := relative(rootPath, movie.PosterPath)
		if e != nil {
			return res, e
		}
		if e = safe(root, p); e != nil {
			return res, e
		}
		indexedPoster = p
		indexedBefore, e = fingerprint(root, p)
		if e != nil {
			return res, e
		}
		if !req.Overwrite && realImage(root, p) {
			res.Skipped = true
			res.Reason = "poster_exists"
			res.PosterPath = movie.PosterPath
			return res, nil
		}
	}
	// Recover a previously installed independent poster after an index error.
	if !req.Overwrite && realImage(files, targetFile) {
		err = db.CompareAndSwapArtwork(ctx, movie, lib.Path, res.PosterPath, func() error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if e := checkRoot(); e != nil {
				return e
			}
			if e := unchanged(files, sourceFile, original, targetFile, before); e != nil {
				return e
			}
			return unchangedPoster(root, indexedPoster, indexedBefore)
		})
		if err != nil {
			return res, err
		}
		res.Changed = true
		res.Reason = "index_repaired"
		return res, nil
	}
	if !req.Overwrite && before.Exists {
		// A damaged independent poster is still somebody else's file. Only an
		// explicit overwrite may replace it; absent targets use atomic Link.
		return res, errors.New("existing private poster requires explicit overwrite")
	}
	var data []byte
	switch req.Mode {
	case "provider":
		if opts.Client == nil {
			return res, errors.New("MetaTube client required")
		}
		data, err = opts.Client.Download(workCtx, opts.Client.ImageURL("primary", req.Provider, req.ProviderID, 90, "", ""), maxImageBytes+1)
	case "screenshot":
		var raw []byte
		raw, err = readLimited(files, sourceFile, 64<<10)
		if err == nil {
			data, err = screenshot(workCtx, strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff")), req, opts)
		}
	}
	if err != nil {
		return res, err
	}
	if err = ctx.Err(); err != nil {
		return res, err
	}
	data, err = validatedJPEG(data)
	if err != nil {
		return res, err
	}
	if err = ctx.Err(); err != nil {
		return res, err
	}
	// Generate an unguessable temporary sibling, never ask ffmpeg to write paths.
	temp, err := createTemp(files, ".")
	if err != nil {
		return res, err
	}
	tempName := filepath.Base(temp.Name())
	defer files.Remove(tempName)
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return res, err
	}
	installed := false
	err = db.CompareAndSwapArtwork(ctx, movie, lib.Path, res.PosterPath, func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := checkRoot(); e != nil {
			return e
		}
		if e := unchanged(files, sourceFile, original, targetFile, before); e != nil {
			return e
		}
		if e := unchangedPoster(root, indexedPoster, indexedBefore); e != nil {
			return e
		}
		if e := installPoster(files, tempName, targetFile, req.Overwrite); e != nil {
			return e
		}
		installed = true
		return nil
	})
	if installed {
		res.Changed = true
	}
	if err != nil && installed {
		res.RefreshError = "poster installed but image index refresh failed; retry artwork operation"
		return res, nil
	}
	return res, err
}

func relative(root, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("artwork path outside library")
	}
	return rel, nil
}
func noSymlinks(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink path refused")
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}
func safe(root *os.Root, path string) error {
	for p := filepath.Clean(path); p != "."; p = filepath.Dir(p) {
		info, err := root.Lstat(p)
		if os.IsNotExist(err) && p == path {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink path refused")
		}
	}
	return nil
}

type stamp struct {
	Exists bool
	Info   os.FileInfo
	Hash   [32]byte
}

// openRegular checks before opening (reject devices/FIFOs), opens without
// blocking or following the final symlink, then verifies the opened identity.
func openRegular(root *os.Root, path string) (*os.File, error) {
	if err := safe(root, path); err != nil {
		return nil, err
	}
	before, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("artwork path is not a regular file")
	}
	f, err := root.OpenFile(path, os.O_RDONLY|regularOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err == nil && (!after.Mode().IsRegular() || !os.SameFile(before, after)) {
		err = store.ErrArtworkChanged
	}
	if err == nil {
		err = safe(root, path)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func fingerprint(root *os.Root, path string) (stamp, error) {
	f, err := openRegular(root, path)
	if os.IsNotExist(err) {
		return stamp{}, nil
	}
	if err != nil {
		return stamp{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return stamp{}, err
	}
	if !st.Mode().IsRegular() {
		return stamp{}, errors.New("artwork path is not a regular file")
	}
	// Source STRMs and posters are bounded before any hashing or decoding.
	if st.Size() > maxImageBytes {
		return stamp{}, errors.New("artwork file exceeds byte limit")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return stamp{}, err
	}
	if n > maxImageBytes {
		return stamp{}, errors.New("artwork file exceeds byte limit")
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return stamp{true, st, sum}, nil
}
func equal(a, b stamp) bool {
	return a.Exists == b.Exists && (!a.Exists || (os.SameFile(a.Info, b.Info) && a.Info.Size() == b.Info.Size() && a.Info.ModTime().Equal(b.Info.ModTime()) && a.Hash == b.Hash))
}
func unchanged(root *os.Root, source string, a stamp, target string, b stamp) error {
	now, e := fingerprint(root, source)
	if e != nil {
		return e
	}
	out, e := fingerprint(root, target)
	if e != nil {
		return e
	}
	if !equal(a, now) || !equal(b, out) {
		return store.ErrArtworkChanged
	}
	return nil
}
func unchangedPoster(root *os.Root, path string, before stamp) error {
	if path == "" {
		return nil
	}
	after, err := fingerprint(root, path)
	if err != nil {
		return err
	}
	if !equal(before, after) {
		return store.ErrArtworkChanged
	}
	return nil
}

func readLimited(root *os.Root, path string, limit int64) ([]byte, error) {
	f, e := openRegular(root, path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if len(b) > int(limit) {
		return nil, errors.New("input exceeds byte limit")
	}
	return b, e
}
func realImage(root *os.Root, path string) bool {
	b, e := readLimited(root, path, maxImageBytes)
	if e != nil {
		return false
	}
	_, e = validatedJPEG(b)
	return e == nil
}
func validatedJPEG(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b) > maxImageBytes {
		return nil, errors.New("invalid image byte size")
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, errors.New("invalid image or pixel limit exceeded")
	}
	img, _, e := image.Decode(bytes.NewReader(b))
	if e != nil {
		return nil, errors.New("invalid image data")
	}
	var out bytes.Buffer
	if e = jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}); e != nil {
		return nil, e
	}
	if out.Len() > maxImageBytes {
		return nil, errors.New("encoded image exceeds byte limit")
	}
	return out.Bytes(), nil
}
func installPoster(root *os.Root, temp, target string, overwrite bool) error {
	if overwrite {
		return root.Rename(temp, target)
	}
	// No check-then-rename: an external producer may create the destination
	// immediately after the final fingerprint. Link is atomic and exclusive.
	// The temporary sibling is removed by Run's deferred cleanup.
	if err := root.Link(temp, target); err != nil {
		if os.IsExist(err) {
			return store.ErrArtworkChanged
		}
		return err
	}
	return nil
}

func createTemp(root *os.Root, dir string) (*os.File, error) {
	for i := 0; i < 10; i++ {
		name := filepath.Join(dir, fmt.Sprintf(".artwork-%s.tmp", rand.Text()))
		f, e := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if e == nil {
			return f, nil
		}
		if !os.IsExist(e) {
			return nil, e
		}
	}
	return nil, errors.New("cannot create artwork temporary file")
}

type cappedBuffer struct {
	// Do not embed bytes.Buffer: its promoted ReadFrom lets io.Copy bypass
	// Write, silently defeating the subprocess output cap.
	buffer     bytes.Buffer
	limit      int
	onOverflow context.CancelFunc
}

func (b *cappedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		if b.onOverflow != nil {
			b.onOverflow()
		}
		return 0, errors.New("process output limit exceeded")
	}
	return b.buffer.Write(p)
}
func command(ctx context.Context, path string, args []string, limit int) ([]byte, error) {
	processCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(processCtx, path, args...)
	containProcess(cmd)
	out := &cappedBuffer{limit: limit, onOverflow: stop}
	diagnostic := &cappedBuffer{limit: 32 << 10, onOverflow: stop}
	cmd.Stdout = out
	cmd.Stderr = diagnostic
	cmd.WaitDelay = 2 * time.Second
	if e := cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("media subprocess failed (check executable and trusted HTTP stream)")
	}
	return out.Bytes(), nil
}
func screenshot(ctx context.Context, raw string, req Request, opts Options) ([]byte, error) {
	if e := trustedStreamURL(raw); e != nil {
		return nil, e
	}
	ffmpeg := opts.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	probe := opts.FFprobePath
	if probe == "" {
		probe = "ffprobe"
	}
	seconds := 0.0
	if req.PositionSeconds != nil {
		seconds = *req.PositionSeconds
	} else {
		pct := 10.0
		if req.PositionPercent != nil {
			pct = *req.PositionPercent
		}
		b, e := command(ctx, probe, []string{"-v", "error", "-protocol_whitelist", protocols, "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", raw}, 4096)
		if e != nil {
			return nil, e
		}
		duration, e := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if e != nil || !finite(duration) || duration <= 0 {
			return nil, errors.New("stream duration unavailable; use explicit seconds")
		}
		seconds = duration * (pct / 100)
	}
	if !finite(seconds) || seconds < 0 || seconds > maxSeekSeconds {
		return nil, errors.New("screenshot position exceeds supported timestamp range")
	}
	return command(ctx, ffmpeg, []string{"-nostdin", "-v", "error", "-protocol_whitelist", protocols, "-max_pixels", strconv.Itoa(maxPixels), "-threads", "1", "-ss", strconv.FormatFloat(seconds, 'f', 3, 64), "-i", raw, "-map", "0:v:0", "-frames:v", "1", "-vf", "scale=1920:1920:force_original_aspect_ratio=decrease", "-threads", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1"}, maxImageBytes)
}

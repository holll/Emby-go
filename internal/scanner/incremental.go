package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"emby-go/internal/nfo"
	"emby-go/internal/store"
)

// ScanMode selects whether unchanged groups may reuse the committed baseline.
type ScanMode string

const (
	ScanModeFull        ScanMode = "full"
	ScanModeIncremental ScanMode = "incremental"
)

// scanFingerprintVersion invalidates every baseline when the interpretation of
// inputs changes: stacking rules, NFO precedence or snapshot fields.
const scanFingerprintVersion int64 = 2

var (
	// ErrScanIncomplete means the inventory could not be proven complete. No
	// missing-source reconciliation may run for such a result.
	ErrScanIncomplete = errors.New("scan incomplete: source read failed")
	// ErrInvalidScanMode is returned before any filesystem work.
	ErrInvalidScanMode = errors.New("invalid scan mode")
	// ErrReconcileBlocked protects an indexed library from a suddenly empty
	// inventory (missing mount). Only an explicit administrator confirmation
	// may remove all records.
	ErrReconcileBlocked = errors.New("scan cleanup blocked: library has indexed movies but the disk inventory is empty")
)

// ScanOptions controls one library inventory pass.
type ScanOptions struct {
	Mode              ScanMode
	AllowEmptyRemoval bool
	// Optional read-only mount identity; both fields must be supplied together.
	RootMarkerPath   string
	RootMarkerSHA256 string
}

type sourceSnapshot struct {
	Path    string `json:"path"`
	Role    string `json:"role"`
	Part    int    `json:"part,omitempty"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_ns"`
	Digest  string `json:"sha256"`
}

type nfoSnapshot struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Digest  string `json:"sha256,omitempty"`
}

type imageSnapshot struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Size    int64  `json:"size,omitempty"`
	MtimeNS int64  `json:"mtime_ns,omitempty"`
}

type groupSnapshot struct {
	Version     int64            `json:"version"`
	LibraryRoot string           `json:"library_root"`
	GroupKey    string           `json:"group_key"`
	Sources     []sourceSnapshot `json:"sources"`
	NFOs        []nfoSnapshot    `json:"nfos"`
	Images      []imageSnapshot  `json:"images"`
}

type memberInput struct {
	candidate candidate
	content   string
	size      int64
	mtimeNS   int64
	digest    string
}

type recordPlan struct {
	member      candidate
	parts       []candidate
	movie       store.Movie
	actors      []store.ActorRef
	validSource bool
	// metaUnreadable 表示侧车文件存在但解析失败。这类条目必须保留已索引的
	// 元数据，只跟随文件刷新派生列，原因写进 last_scrape_error。
	metaUnreadable bool
	metaError      string
}

type groupInputs struct {
	key      string
	library  store.Library
	members  []memberInput
	stacked  bool
	primary  int
	parts    []int
	nfos     map[string]nfoFileInput // key: "sidecar:"|shared:" path"
	records  []recordPlan
	images   []imageSnapshot
	snapshot groupSnapshot
}

// ScanWithOptions enumerates the full disk inventory and, in incremental mode,
// skips groups whose fingerprint and index evidence still match. Missing-source
// reconciliation runs only after a provably complete inventory.
func ScanWithOptions(ctx context.Context, s *store.Store, lib store.Library, opts ScanOptions, onProgress func(Progress)) (Result, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ScanModeFull
	}
	if mode != ScanModeFull && mode != ScanModeIncremental {
		return Result{}, ErrInvalidScanMode
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := Result{Mode: string(mode)}
	fail := func(err error) (Result, error) { result.Incomplete = true; return result, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	rootInfo, err := os.Stat(lib.Path)
	if err != nil {
		return fail(err)
	}
	if !rootInfo.IsDir() {
		return fail(fmt.Errorf("library root is not a directory: %s", lib.Path))
	}
	health := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := s.Library(lib.ID)
		if err != nil {
			return err
		}
		if current.Path != lib.Path {
			return store.ErrScanLibraryChanged
		}
		info, err := os.Stat(lib.Path)
		if err != nil {
			return err
		}
		if !info.IsDir() || !os.SameFile(rootInfo, info) {
			return fmt.Errorf("library root changed: %s", lib.Path)
		}
		return validateRootMarker(lib.Path, opts)
	}
	if err := health(); err != nil {
		return fail(err)
	}
	manualPaths, err := s.ManualSourcePaths(lib.ID)
	if err != nil {
		return fail(fmt.Errorf("manual sources: %w", err))
	}
	previousCount, err := s.LibraryMovieCount(lib.ID)
	if err != nil {
		return fail(fmt.Errorf("movie count: %w", err))
	}
	states, err := s.LoadScanGroups(lib.ID)
	if err != nil {
		return fail(fmt.Errorf("load groups: %w", err))
	}
	evidence, err := s.LoadScanIndexEvidence(lib.ID)
	if err != nil {
		return fail(fmt.Errorf("index evidence: %w", err))
	}
	groups, err := enumerateGroupsContext(ctx, lib)
	if err != nil {
		return fail(fmt.Errorf("enumerate: %w", err))
	}
	total := len(groups)
	result.GroupsTotal = total
	report := func(done int, current string) {
		if onProgress != nil {
			onProgress(Progress{LibraryID: lib.ID, LibraryName: lib.Name, Total: total, Done: done, Current: current, Result: result})
		}
	}
	report(0, "")

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	expected := make(map[string]int64)
	committedKeys := make([]string, 0, len(keys))
	complete := true
	for index, key := range keys {
		if ctxErr := ctx.Err(); ctxErr != nil {
			result.Incomplete = true
			return result, ctxErr
		}
		group := groups[key]
		inputs, readErr := readGroupInputs(lib, key, group)
		if readErr != nil {
			result.Failed++
			result.Incomplete = true
			complete = false
			result.GroupsDone = index + 1
			report(index+1, key)
			continue
		}
		fingerprint, fpErr := inputs.fingerprint()
		if fpErr != nil {
			return result, fpErr
		}
		if !inputs.stillMatches() {
			result.Failed++
			result.Incomplete = true
			complete = false
			result.GroupsDone = index + 1
			report(index+1, key)
			continue
		}
		if err := health(); err != nil {
			return fail(err)
		}
		if mode == ScanModeIncremental {
			if state, ok := states[key]; ok &&
				state.FingerprintVersion == scanFingerprintVersion &&
				state.Fingerprint == fingerprint &&
				scanEvidenceMatches(state, evidence) {
				for _, source := range state.Sources {
					if source.ExpectedMovieID > 0 {
						expected[source.Path] = source.ExpectedMovieID
					}
				}
				committedKeys = append(committedKeys, key)
				result.UnchangedGroups++
				result.GroupsDone = index + 1
				report(index+1, key)
				continue
			}
		}
		built, buildErr := buildRecords(lib, inputs, manualPaths)
		if buildErr != nil {
			result.Failed++
			result.Incomplete = true
			complete = false
			result.GroupsDone = index + 1
			report(index+1, key)
			continue
		}
		if !inputs.stillMatches() {
			result.Failed++
			result.Incomplete = true
			complete = false
			result.GroupsDone = index + 1
			report(index+1, key)
			continue
		}
		movieEntries := make([]store.ScanGroupMovie, 0, len(built))
		// Record every member, including stacked parts and skipped dependencies:
		// evidence checks compare the primary's additional_parts against this list.
		sourceEntries := make([]store.ScanGroupSource, 0, len(inputs.members))
		outcome := "skipped"
		primaryPath := ""
		if inputs.stacked {
			primaryPath = inputs.members[inputs.primary].candidate.path
		}
		for _, member := range inputs.members {
			source := store.ScanGroupSource{Path: member.candidate.path, Role: "standalone"}
			if inputs.stacked {
				if member.candidate.path == primaryPath {
					source.Role = "primary"
				} else {
					source.Role = "part"
				}
			}
			sourceEntries = append(sourceEntries, source)
		}
		for _, record := range built {
			if !record.validSource {
				continue
			}
			member := memberFor(inputs, record.member.path)
			movieEntries = append(movieEntries, store.ScanGroupMovie{Movie: record.movie, Size: member.size, Mtime: time.Unix(0, member.mtimeNS), Actors: record.actors,
				KeepIndexedMetadata: record.metaUnreadable, MetadataError: record.metaError})
			if record.movie.Status == "success" {
				outcome = "success"
			} else if outcome != "success" {
				outcome = "pending"
			}
		}
		if err := health(); err != nil {
			return fail(err)
		}
		commit, commitErr := s.CommitScanGroupCheckedContext(ctx, lib, key, fingerprint, scanFingerprintVersion, outcome, movieEntries, sourceEntries)
		if commitErr != nil {
			result.Incomplete = true
			return result, commitErr
		}
		committedKeys = append(committedKeys, key)
		// Count only records whose transaction committed. A failed group must not
		// inflate success/pending in the partial result returned to the caller.
		for _, record := range built {
			switch {
			case manualPaths[record.member.path] && commit.MovieIDs[record.member.path] > 0:
				result.Success++
			case !record.validSource:
				result.Skipped++
			case record.metaUnreadable:
				result.UnreadableNFO++
			case record.movie.Status == "success" || record.movie.Status == "manual":
				result.Success++
			default:
				result.Pending++
			}
		}
		result.Added += commit.Added
		result.Updated += commit.Updated
		result.Deleted += commit.Deleted
		result.IndexChanged = result.Added+result.Updated+result.Deleted > 0
		for path, id := range commit.MovieIDs {
			expected[path] = id
		}
		result.GroupsDone = index + 1
		report(index+1, key)
	}
	if result.Failed > 0 || !complete {
		result.Incomplete = true
		return result, ErrScanIncomplete
	}
	if err := health(); err != nil {
		return fail(err)
	}
	finalGroups, err := enumerateGroupsContext(ctx, lib)
	if err != nil {
		return fail(err)
	}
	if !sameInventory(groups, finalGroups) {
		return fail(ErrScanIncomplete)
	}
	if err := health(); err != nil {
		return fail(err)
	}
	if previousCount > 0 && total == 0 && !opts.AllowEmptyRemoval {
		result.Incomplete = true
		return result, ErrReconcileBlocked
	}
	reconcile, reconcileErr := s.ReconcileScanInventoryCheckedContext(ctx, lib, expected, committedKeys)
	if reconcileErr != nil {
		return fail(reconcileErr)
	}
	result.CleanupPerformed = true
	result.Deleted += reconcile.DeletedMovies
	result.IndexChanged = result.Added+result.Updated+result.Deleted > 0
	return result, nil
}

// Merge folds a per-library result into a multi-library total. Counters are
// additive; flags accumulate; mode keeps the first non-empty value.
func (r *Result) Merge(other Result) {
	if r.Mode == "" {
		r.Mode = other.Mode
	}
	r.Success += other.Success
	r.Pending += other.Pending
	r.Skipped += other.Skipped
	r.Failed += other.Failed
	r.GroupsTotal += other.GroupsTotal
	r.GroupsDone += other.GroupsDone
	r.UnchangedGroups += other.UnchangedGroups
	r.Added += other.Added
	r.Updated += other.Updated
	r.Deleted += other.Deleted
	r.IndexChanged = r.IndexChanged || other.IndexChanged
	r.CleanupPerformed = r.CleanupPerformed || other.CleanupPerformed
	r.Incomplete = r.Incomplete || other.Incomplete
	r.IndexedSources = append(r.IndexedSources, other.IndexedSources...)
}

func enumerateGroups(lib store.Library) (map[string][]candidate, error) {
	return enumerateGroupsContext(context.Background(), lib)
}

func enumerateGroupsContext(ctx context.Context, lib store.Library) (map[string][]candidate, error) {
	groups := make(map[string][]candidate)
	err := filepath.Walk(lib.Path, func(path string, info os.FileInfo, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == lib.Path && !info.IsDir() {
			return errors.New("媒体库路径不是目录: " + lib.Path)
		}
		if info.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".strm") {
			return nil
		}
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		item := candidate{path: path, info: info, base: base}
		if match := cdPartPattern.FindStringSubmatch(base); match != nil {
			item.base = match[1]
			item.part = parsePart(match[2])
			item.groupKey = filepath.Join(filepath.Dir(path), strings.ToLower(match[1]))
		} else {
			item.groupKey = path
		}
		groups[item.groupKey] = append(groups[item.groupKey], item)
		return nil
	})
	return groups, err
}

func scanEvidenceMatches(state store.ScanGroupState, evidence map[string]store.ScanMovieEvidence) bool {
	primaryParts := make([]string, 0)
	for _, source := range state.Sources {
		if source.Role == "part" {
			primaryParts = append(primaryParts, source.Path)
		}
		if source.ExpectedMovieID == 0 {
			if _, exists := evidence[source.Path]; exists {
				return false
			}
			continue // skipped sources and ordinary parts must not own movie rows
		}
		item, ok := evidence[source.Path]
		if !ok || item.ID != source.ExpectedMovieID {
			return false
		}
	}
	// A group whose every source is a dependency (invalid protocol) scores no
	// movie evidence; require that no indexed row exists for those paths.
	if len(state.Sources) > 0 && allSourcesWithoutMovies(state.Sources) {
		for _, source := range state.Sources {
			if _, ok := evidence[source.Path]; ok {
				return false
			}
		}
	}
	for _, source := range state.Sources {
		if source.Role == "part" {
			continue
		}
		wantParts := []string(nil)
		if source.Role == "primary" {
			wantParts = primaryParts
		}
		if item, ok := evidence[source.Path]; ok && !sameStringSet(item.AdditionalParts, wantParts) {
			return false
		}
	}
	return true
}

func allSourcesWithoutMovies(sources []store.ScanGroupSource) bool {
	for _, source := range sources {
		if source.ExpectedMovieID > 0 {
			return false
		}
	}
	return true
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func readGroupInputs(lib store.Library, key string, group []candidate) (*groupInputs, error) {
	sorted := append([]candidate(nil), group...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].part != sorted[j].part {
			return sorted[i].part < sorted[j].part
		}
		return sorted[i].path < sorted[j].path
	})
	inputs := &groupInputs{key: key, library: lib, nfos: make(map[string]nfoFileInput)}
	for _, item := range sorted {
		member, err := readMemberInput(item)
		if err != nil {
			return nil, err
		}
		inputs.members = append(inputs.members, member)
	}
	primary, parts, ok := stackedGroup(sorted)
	if ok {
		inputs.stacked = true
		for i, member := range inputs.members {
			if member.candidate.path == primary.path {
				inputs.primary = i
			}
		}
		for _, part := range parts {
			for i, member := range inputs.members {
				if member.candidate.path == part.path {
					inputs.parts = append(inputs.parts, i)
				}
			}
		}
	}
	if inputs.stacked {
		record := recordPlan{member: inputs.members[inputs.primary].candidate}
		for _, partIndex := range inputs.parts {
			record.parts = append(record.parts, inputs.members[partIndex].candidate)
		}
		inputs.records = []recordPlan{record}
	} else {
		for _, member := range inputs.members {
			inputs.records = append(inputs.records, recordPlan{member: member.candidate})
		}
	}
	for i := range inputs.records {
		record := &inputs.records[i]
		sidecar := nfo.SidecarPath(record.member.path, false)
		shared := nfo.SidecarPath(record.member.path, true)
		sidecarInput, err := readNFOFile(sidecar)
		if err != nil {
			return nil, err
		}
		inputs.nfos["sidecar:"+sidecar] = sidecarInput
		if len(record.parts) > 0 {
			sharedInput, err := readNFOFile(shared)
			if err != nil {
				return nil, err
			}
			inputs.nfos["shared:"+shared] = sharedInput
		}
		record.movie = store.Movie{LibraryID: lib.ID, SourcePath: record.member.path, OutputDir: filepath.Dir(record.member.path), Status: "pending", AdditionalParts: partPaths(record.parts)}
		images, err := snapshotImages(record.movie.OutputDir)
		if err != nil {
			return nil, err
		}
		inputs.images = append(inputs.images, images...)

	}
	return inputs, nil
}

func partPaths(parts []candidate) []string {
	if len(parts) == 0 {
		return nil
	}
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		paths = append(paths, part.path)
	}
	return paths
}

type nfoFileInput struct {
	path     string
	present  bool
	missing  bool
	digest   string
	data     []byte
	meta     nfo.MovieMeta
	parseErr error
}

func readNFOFile(path string) (nfoFileInput, error) {
	for attempt := 0; attempt < 2; attempt++ {
		before, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nfoFileInput{path: path, missing: true}, nil
		}
		if err != nil {
			return nfoFileInput{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nfoFileInput{}, err
		}
		after, err := os.Stat(path)
		if err != nil {
			return nfoFileInput{}, err
		}
		if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			continue
		}
		sum := sha256.Sum256(data)
		return nfoFileInput{path: path, present: true, digest: hex.EncodeToString(sum[:]), data: data}, nil
	}
	return nfoFileInput{}, fmt.Errorf("NFO changed during read: %s", path)
}

func sameInventory(a, b map[string][]candidate) bool {
	if len(a) != len(b) {
		return false
	}
	for key, group := range a {
		other, ok := b[key]
		if !ok || len(group) != len(other) {
			return false
		}
		paths := make(map[string]bool, len(group))
		for _, item := range group {
			paths[item.path] = true
		}
		for _, item := range other {
			if !paths[item.path] {
				return false
			}
		}
	}
	return true
}

func (n nfoFileInput) missingOrInvalid() bool { return !n.present || n.parseErr != nil }

// snapshotImages mirrors imageutil's ordered selection, but does not hide I/O errors.
func snapshotImages(dir string) ([]imageSnapshot, error) {
	var result []imageSnapshot
	for _, kind := range []string{"poster", "backdrop", "landscape"} {
		stems := []string{kind}
		if kind == "poster" {
			stems = []string{"poster", "folder", "cover", "default"}
		}
		if kind == "backdrop" {
			stems = []string{"fanart"}
		}
		entry := imageSnapshot{Kind: kind}
		for _, stem := range stems {
			for _, ext := range []string{".webp", ".jpg", ".jpeg", ".png", ".JPG", ".JPEG", ".PNG", ".WebP", ".WEBP"} {
				path := filepath.Join(dir, stem+ext)
				info, err := os.Stat(path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				entry = imageSnapshot{Kind: kind, Path: path, Present: true, Size: info.Size(), MtimeNS: info.ModTime().UnixNano()}
				break
			}
			if entry.Present {
				break
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

func (inputs *groupInputs) fingerprint() (string, error) {
	snapshot := groupSnapshot{Version: scanFingerprintVersion, LibraryRoot: filepath.Clean(inputs.library.Path), GroupKey: inputs.key}
	for _, member := range inputs.members {
		role := "standalone"
		if inputs.stacked {
			role = "part"
			if member.candidate.path == inputs.members[inputs.primary].candidate.path {
				role = "primary"
			}
		}
		snapshot.Sources = append(snapshot.Sources, sourceSnapshot{Path: member.candidate.path, Role: role, Part: member.candidate.part, Size: member.size, MtimeNS: member.mtimeNS, Digest: member.digest})
	}
	sort.Slice(snapshot.Sources, func(i, j int) bool { return snapshot.Sources[i].Path < snapshot.Sources[j].Path })
	seenNFO := make(map[string]bool)
	for key, file := range inputs.nfos {
		kind := strings.SplitN(key, ":", 2)[0]
		if seenNFO[kind+file.path] {
			continue
		}
		seenNFO[kind+file.path] = true
		snapshot.NFOs = append(snapshot.NFOs, nfoSnapshot{Kind: kind, Path: file.path, Present: file.present, Digest: file.digest})
	}
	sort.Slice(snapshot.NFOs, func(i, j int) bool {
		if snapshot.NFOs[i].Path != snapshot.NFOs[j].Path {
			return snapshot.NFOs[i].Path < snapshot.NFOs[j].Path
		}
		return snapshot.NFOs[i].Kind < snapshot.NFOs[j].Kind
	})
	seenImage := make(map[string]bool)
	for _, image := range inputs.images {
		if seenImage[image.Kind+image.Path] {
			continue
		}
		seenImage[image.Kind+image.Path] = true
		snapshot.Images = append(snapshot.Images, image)
	}
	sort.Slice(snapshot.Images, func(i, j int) bool {
		if snapshot.Images[i].Kind != snapshot.Images[j].Kind {
			return snapshot.Images[i].Kind < snapshot.Images[j].Kind
		}
		return snapshot.Images[i].Path < snapshot.Images[j].Path
	})
	inputs.snapshot = snapshot
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (inputs *groupInputs) stillMatches() bool {
	for _, member := range inputs.members {
		current, err := readMemberInput(member.candidate)
		if err != nil || current.size != member.size || current.mtimeNS != member.mtimeNS || current.digest != member.digest {
			return false
		}
	}
	for _, file := range inputs.nfos {
		if !file.present {
			if _, err := os.Stat(file.path); !errors.Is(err, os.ErrNotExist) {
				return false
			}
			continue
		}
		data, err := os.ReadFile(file.path)
		if err != nil {
			return false
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != file.digest {
			return false
		}
	}
	for _, record := range inputs.records {
		images, err := snapshotImages(record.movie.OutputDir)
		if err != nil {
			return false
		}
		for _, image := range images {
			found := false
			for _, old := range inputs.images {
				if old == image {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	// Detect new or removed CD members, not merely changes to existing paths.
	if len(inputs.members) > 0 && inputs.members[0].candidate.part > 0 {
		target := inputs.members[0].candidate
		entries, err := os.ReadDir(filepath.Dir(target.path))
		if err != nil {
			return false
		}
		var current []candidate
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".strm") {
				continue
			}
			base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			match := cdPartPattern.FindStringSubmatch(base)
			if match == nil || !strings.EqualFold(match[1], target.base) {
				continue
			}
			if _, err := entry.Info(); err != nil {
				return false
			}
			current = append(current, candidate{path: filepath.Join(filepath.Dir(target.path), entry.Name())})
		}
		if len(current) != len(inputs.members) {
			return false
		}
		for _, item := range current {
			found := false
			for _, member := range inputs.members {
				if member.candidate.path == item.path {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func buildRecords(lib store.Library, inputs *groupInputs, manualPaths map[string]bool) ([]recordPlan, error) {
	if len(inputs.records) == 0 {
		return nil, fmt.Errorf("empty group %s", inputs.key)
	}
	for i := range inputs.records {
		record := &inputs.records[i]
		var member memberInput
		for _, candidate := range inputs.members {
			if candidate.candidate.path == record.member.path {
				member = candidate
				break
			}
		}
		if member.candidate.path == "" {
			return nil, fmt.Errorf("source vanished from group %s", inputs.key)
		}
		record.validSource = ValidHTTP(member.content)
		if !record.validSource {
			continue
		}
		selected := inputs.nfos["sidecar:"+nfo.SidecarPath(record.member.path, false)]
		if selected.present {
			selected.meta, selected.parseErr = nfo.ReadBytes(selected.data)
		}
		// 记住「文件在但读不出来」的候选：换用共享 NFO 成功后才清掉这个标记。
		unreadablePath, unreadableErr := "", error(nil)
		if selected.present && selected.parseErr != nil {
			unreadablePath, unreadableErr = selected.path, selected.parseErr
		}
		if selected.missingOrInvalid() && len(record.parts) > 0 {
			selected = inputs.nfos["shared:"+nfo.SidecarPath(record.member.path, true)]
			if selected.present {
				selected.meta, selected.parseErr = nfo.ReadBytes(selected.data)
			}
			if selected.present && selected.parseErr == nil {
				unreadablePath, unreadableErr = "", nil
			} else if selected.present {
				unreadablePath, unreadableErr = selected.path, selected.parseErr
			}
		}
		if unreadablePath != "" {
			// 侧车存在但读不出内容：保留上一版元数据，别让空构建清空索引。
			record.metaUnreadable = true
			record.metaError = nfoNote(unreadablePath, unreadableErr)
		}
		if selected.present && selected.parseErr == nil {
			applyMeta(&record.movie, selected.meta, selected.path)
			for _, actor := range selected.meta.Actors {
				record.actors = append(record.actors, store.ActorRef{Name: actor.Name, AvatarURL: strings.TrimSpace(actor.Thumb)})
			}
			for _, image := range inputs.images {
				switch image.Kind {
				case "poster":
					record.movie.PosterPath = image.Path
				case "backdrop":
					record.movie.BackdropPath = image.Path
				case "landscape":
					record.movie.LandscapePath = image.Path
				}
			}
		}
		setSource(&record.movie, member.content)
		if record.movie.Status == "" {
			record.movie.Status = "pending"
		}
		if manualPaths[record.member.path] && record.movie.Status == "success" {
			record.movie.Status = "manual"
		}
	}
	return inputs.records, nil
}

func readMemberInput(item candidate) (memberInput, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		before, err := os.Stat(item.path)
		if err != nil {
			return memberInput{}, err
		}
		data, err := os.ReadFile(item.path)
		if err != nil {
			return memberInput{}, err
		}
		after, err := os.Stat(item.path)
		if err != nil {
			return memberInput{}, err
		}
		if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			lastErr = fmt.Errorf("source changed during read: %s", item.path)
			continue
		}
		line := firstLine(string(data))
		if line == "" && !strings.Contains(string(data), "\n") {
			return memberInput{}, fmt.Errorf("empty source file: %s", item.path)
		}
		sum := sha256.Sum256(data)
		return memberInput{candidate: item, content: line, size: after.Size(), mtimeNS: after.ModTime().UnixNano(), digest: hex.EncodeToString(sum[:])}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unstable source file: %s", item.path)
	}
	return memberInput{}, lastErr
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

func memberFor(inputs *groupInputs, path string) memberInput {
	for _, member := range inputs.members {
		if member.candidate.path == path {
			return member
		}
	}
	return memberInput{}
}

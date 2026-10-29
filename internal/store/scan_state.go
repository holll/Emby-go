package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ScanGroupSource is one disk dependency recorded for a committed group.
// ExpectedMovieID is zero for dependencies that produce no movie row. A part
// may still retain a positive ID when an existing manual record is preserved.
type ScanGroupSource struct {
	Path            string
	Role            string
	ExpectedMovieID int64
}

// ScanGroupState is the last committed baseline for one logical movie group.
type ScanGroupState struct {
	GroupKey           string
	FingerprintVersion int64
	Fingerprint        string
	Outcome            string
	Sources            []ScanGroupSource
}

// ScanGroupMovie is one indexed record produced by a group build. The same
// transaction writes every movie, its actors, features and the new baseline.
type ScanGroupMovie struct {
	Movie  Movie
	Size   int64
	Mtime  time.Time
	Actors []ActorRef
	// KeepIndexedMetadata 标记「NFO 存在但无法解析」。这类条目只跟随文件刷新
	// 派生列：已索引的元数据、演员、特征与可见状态原样保留，空构建不是
	// 「清空索引」指令。MetadataError 记录原因，写入 last_scrape_error。
	KeepIndexedMetadata bool
	MetadataError       string
}

// ScanNFONotePrefix 标记扫描期「NFO 存在但无法解析」的备注。它与刮削失败、
// 批量确认共用 last_scrape_error 一列（不新增 status 枚举），但前缀独立：
// NFO 恢复可解析后由扫描清除，不干扰刮削结论。
const ScanNFONotePrefix = "[NFO 不可读] "

// ScanGroupCommitResult reports the business rows touched by one group commit.
type ScanGroupCommitResult struct {
	MovieIDs map[string]int64
	Added    int
	Updated  int
	Deleted  int
}

// ScanMovieEvidence is the part of a movie row that can prove an unchanged
// baseline still describes the live index.
type ScanMovieEvidence struct {
	ID              int64
	AdditionalParts []string
}

// LoadScanGroups returns all committed group baselines for a library.
func (s *Store) LoadScanGroups(libraryID int64) (map[string]ScanGroupState, error) {
	rows, err := s.db.Query(`SELECT g.group_key, g.fingerprint_version, g.fingerprint, g.outcome,
		COALESCE(s.source_path, ''), COALESCE(s.role, ''), COALESCE(s.expected_movie_id, 0)
		FROM scan_groups g
		LEFT JOIN scan_group_sources s ON s.library_id = g.library_id AND s.group_key = g.group_key
		WHERE g.library_id = ?
		ORDER BY g.group_key, s.source_path`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]ScanGroupState)
	for rows.Next() {
		var key string
		var fingerprintVersion int64
		var fingerprint, outcome, path, role string
		var expectedID int64
		if err := rows.Scan(&key, &fingerprintVersion, &fingerprint, &outcome, &path, &role, &expectedID); err != nil {
			return nil, err
		}
		state, ok := states[key]
		if !ok {
			state = ScanGroupState{GroupKey: key, FingerprintVersion: fingerprintVersion, Fingerprint: fingerprint, Outcome: outcome}
		}
		if path != "" {
			state.Sources = append(state.Sources, ScanGroupSource{Path: path, Role: role, ExpectedMovieID: expectedID})
		}
		states[key] = state
	}
	return states, rows.Err()
}

// LoadScanIndexEvidence loads every movie row for a library, keyed by source
// path. Scanner compares this evidence with the persisted source list; an
// unrelated rows rewrite invalidates the baseline through the write helpers.
func (s *Store) LoadScanIndexEvidence(libraryID int64) (map[string]ScanMovieEvidence, error) {
	rows, err := s.db.Query(`SELECT id, source_path, additional_parts FROM movies WHERE library_id = ?`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidence := make(map[string]ScanMovieEvidence)
	for rows.Next() {
		var item ScanMovieEvidence
		var path, parts string
		if err := rows.Scan(&item.ID, &path, &parts); err != nil {
			return nil, err
		}
		item.AdditionalParts = decodeAdditionalParts(parts)
		evidence[path] = item
	}
	return evidence, rows.Err()
}

func decodeAdditionalParts(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var parts []string
	if err := json.Unmarshal([]byte(text), &parts); err != nil {
		return nil
	}
	return parts
}

// CommitScanGroup writes every movie of one logical group, removes records that
// the new grouping retired, and atomically replaces the baseline. Movies are
// upserted with the caller's already-read input; no file I/O happens here.
func (s *Store) CommitScanGroup(libraryID int64, groupKey, fingerprint string, fingerprintVersion int64, outcome string, movies []ScanGroupMovie, sources []ScanGroupSource) (ScanGroupCommitResult, error) {
	return s.commitScanGroup(context.Background(), Library{ID: libraryID}, false, groupKey, fingerprint, fingerprintVersion, outcome, movies, sources)
}

// ErrScanLibraryChanged rejects inputs read for a deleted or relocated library.
var ErrScanLibraryChanged = errors.New("scan library configuration changed")

// ErrScanInventoryChanged rejects stale or incomplete index evidence at cleanup.
var ErrScanInventoryChanged = errors.New("scan index inventory changed")

func (s *Store) CommitScanGroupChecked(lib Library, groupKey, fingerprint string, fingerprintVersion int64, outcome string, movies []ScanGroupMovie, sources []ScanGroupSource) (ScanGroupCommitResult, error) {
	return s.CommitScanGroupCheckedContext(context.Background(), lib, groupKey, fingerprint, fingerprintVersion, outcome, movies, sources)
}

// CommitScanGroupCheckedContext checks cancellation after obtaining the writer
// and before any irreversible commit; all tentative work rolls back on cancel.
func (s *Store) CommitScanGroupCheckedContext(ctx context.Context, lib Library, groupKey, fingerprint string, fingerprintVersion int64, outcome string, movies []ScanGroupMovie, sources []ScanGroupSource) (ScanGroupCommitResult, error) {
	return s.commitScanGroup(ctx, lib, true, groupKey, fingerprint, fingerprintVersion, outcome, movies, sources)
}

// reserveScanWriterTx acquires SQLite's writer reservation without changing any
// row, firing row triggers, or advancing business versions. Unlike a version
// bump, this is also safe for state-only commits and unchanged reconciliation.
func reserveScanWriterTx(tx *sql.Tx) error {
	return reserveScanWriterTxContext(context.Background(), tx)
}

func reserveScanWriterTxContext(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE scan_groups SET fingerprint = fingerprint WHERE 0`)
	return err
}

func validateScanLibraryTx(tx *sql.Tx, lib Library, checkPath bool) error {
	var path string
	err := tx.QueryRow(`SELECT path FROM libraries WHERE id = ?`, lib.ID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && checkPath && path != lib.Path) {
		return ErrScanLibraryChanged
	}
	return err
}

func (s *Store) commitScanGroup(ctx context.Context, lib Library, checkPath bool, groupKey, fingerprint string, fingerprintVersion int64, outcome string, movies []ScanGroupMovie, sources []ScanGroupSource) (ScanGroupCommitResult, error) {
	libraryID := lib.ID
	if groupKey == "" || fingerprint == "" || fingerprintVersion <= 0 || len(sources) == 0 {
		return ScanGroupCommitResult{}, ErrScanInventoryChanged
	}
	sourcePaths := make([]string, 0, len(sources))
	seenSources := make(map[string]bool, len(sources))
	for _, source := range sources {
		if source.Path == "" || seenSources[source.Path] || source.ExpectedMovieID < 0 {
			return ScanGroupCommitResult{}, ErrScanInventoryChanged
		}
		seenSources[source.Path] = true
		sourcePaths = append(sourcePaths, source.Path)
	}
	seenMovies := make(map[string]bool, len(movies))
	for _, entry := range movies {
		if entry.Movie.LibraryID != libraryID || !seenSources[entry.Movie.SourcePath] || seenMovies[entry.Movie.SourcePath] {
			return ScanGroupCommitResult{}, ErrScanInventoryChanged
		}
		seenMovies[entry.Movie.SourcePath] = true
	}
	result := ScanGroupCommitResult{MovieIDs: make(map[string]int64)}
	var version int64
	err := s.writeTx(func(tx *sql.Tx) error {
		result = ScanGroupCommitResult{MovieIDs: make(map[string]int64)}
		version = 0
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := reserveScanWriterTxContext(ctx, tx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateScanLibraryTx(tx, lib, checkPath); err != nil {
			return err
		}
		// A source changing groups retires every overlapping old baseline before
		// the replacement is saved. Otherwise a later old-group commit could
		// delete a retained movie through obsolete expected_movie_id evidence.
		if err := InvalidateScanGroupsForPathsTx(tx, libraryID, sourcePaths); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM scan_groups WHERE library_id = ? AND group_key = ?`, libraryID, groupKey); err != nil {
			return err
		}

		for _, entry := range movies {
			if err := ctx.Err(); err != nil {
				return err
			}
			var previousID, previousLibrary sql.NullInt64
			var previousStatus string
			if err := tx.QueryRow(`SELECT id, library_id, status FROM movies WHERE source_path = ?`, entry.Movie.SourcePath).Scan(&previousID, &previousLibrary, &previousStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if entry.KeepIndexedMetadata && previousID.Valid {
				// The sidecar exists but cannot be parsed. That is a read failure,
				// not an instruction to erase metadata: keep the row visible with
				// what is already indexed and only follow the file.
				if !previousLibrary.Valid || previousLibrary.Int64 != libraryID {
					return ErrScanInventoryChanged
				}
				changed, err := preserveIndexedMetadataTx(tx, previousID.Int64, entry)
				if err != nil {
					return err
				}
				if changed {
					result.Updated++
				}
				result.MovieIDs[entry.Movie.SourcePath] = previousID.Int64
				continue
			}
			if previousStatus == "manual" {
				// A missing/invalid NFO produces an empty pending build, not an
				// instruction to erase operator metadata, actors or features.
				if entry.Movie.Status != "success" && entry.Movie.Status != "manual" {
					if !previousLibrary.Valid || previousLibrary.Int64 != libraryID {
						return ErrScanInventoryChanged
					}
					changed, err := updateManualScanStructureTx(tx, previousID.Int64, entry)
					if err != nil {
						return err
					}
					if changed {
						result.Updated++
					}
					result.MovieIDs[entry.Movie.SourcePath] = previousID.Int64
					continue
				}
				entry.Movie.Status = "manual"
			}
			if previousLibrary.Valid && previousLibrary.Int64 != libraryID {
				if err := InvalidateScanGroupsForPathsTx(tx, previousLibrary.Int64, []string{entry.Movie.SourcePath}); err != nil {
					return err
				}
				if err := bumpLibraryVersionTx(tx, previousLibrary.Int64); err != nil {
					return err
				}
			}
			id, err := upsertMovieTx(tx, entry.Movie, entry.Size, entry.Mtime)
			if err != nil {
				return err
			}
			if err := replaceActorsTx(tx, id, entry.Actors); err != nil {
				return err
			}
			if err := refreshFeaturesTx(tx, id); err != nil {
				return err
			}
			if entry.KeepIndexedMetadata {
				// New row for an unreadable sidecar: it is legitimately pending,
				// but the reason must be visible to the operator.
				if err := setScanNoteTx(tx, id, entry.MetadataError); err != nil {
					return err
				}
			} else if entry.Movie.Status == "success" || entry.Movie.Status == "manual" {
				// Metadata refreshed normally; a previous "unreadable" note is stale.
				if err := clearScanNoteTx(tx, id); err != nil {
					return err
				}
			}
			if previousID.Valid {
				result.Updated++
			} else {
				result.Added++
			}
			result.MovieIDs[entry.Movie.SourcePath] = id
		}

		// Still-present manual records stay under operator control, including
		// a CD2 that has just become another movie's part. Carry their identity
		// into both the result and baseline so final reconciliation retains it.
		for _, source := range sources {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, keeps := result.MovieIDs[source.Path]; keeps {
				continue
			}
			var id int64
			err := tx.QueryRow(`SELECT id FROM movies WHERE library_id = ? AND source_path = ? AND status = 'manual'`, libraryID, source.Path).Scan(&id)
			if err == nil {
				result.MovieIDs[source.Path] = id
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}

		// Do not delete paths merely absent from this replacement group. They
		// may have moved into a different current group that has not committed
		// yet. Only final, complete-inventory reconciliation can prove absence.

		// A group's own source paths may still own rows that were indexed by an
		// earlier code path and therefore have no scan_group_sources evidence
		// (protocol changes, legacy CD part rows). Remove them now that the
		// group has been re-evaluated. Manual rows stay under operator control.
		for _, source := range sources {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, keeps := result.MovieIDs[source.Path]; keeps {
				continue
			}
			res, err := tx.Exec(`DELETE FROM movies WHERE library_id = ? AND source_path = ? AND status <> 'manual'`, libraryID, source.Path)
			if err != nil {
				return err
			}
			removed, err := res.RowsAffected()
			if err != nil {
				return err
			}
			result.Deleted += int(removed)
		}

		if _, err := tx.Exec(`INSERT INTO scan_groups(library_id, group_key, fingerprint_version, fingerprint, outcome) VALUES(?,?,?,?,?)`,
			libraryID, groupKey, fingerprintVersion, fingerprint, outcome); err != nil {
			return err
		}
		for _, source := range sources {
			expected := result.MovieIDs[source.Path]
			if source.ExpectedMovieID != 0 && source.ExpectedMovieID != expected {
				return ErrScanInventoryChanged
			}
			if _, err := tx.Exec(`INSERT INTO scan_group_sources(library_id, group_key, source_path, role, expected_movie_id) VALUES(?,?,?,?,?)`,
				libraryID, groupKey, source.Path, source.Role, nullableID(expected)); err != nil {
				return err
			}
		}

		if result.Added+result.Updated+result.Deleted > 0 {
			var err error
			version, err = bumpVersionTx(tx, libraryID)
			if err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return ScanGroupCommitResult{MovieIDs: map[string]int64{}}, err
	}
	if version > 0 {
		s.publishVersion(version)
	}
	return result, nil
}

// updateManualScanStructureTx refreshes disk-derived playback structure without
// replacing operator metadata or its actor/feature projections. Its predicate
// avoids touching updated_at or versions when those structural fields match.
func updateManualScanStructureTx(tx *sql.Tx, id int64, entry ScanGroupMovie) (bool, error) {
	m := entry.Movie
	parts, mtime := jsonText(m.AdditionalParts), entry.Mtime.UTC().Format(time.RFC3339)
	result, err := tx.Exec(`UPDATE movies SET file_size=?,file_mtime=?,source_protocol=?,source_container=?,additional_parts=?,updated_at=?
  WHERE id=? AND (COALESCE(file_size,0)<>? OR COALESCE(file_mtime,'')<>? OR COALESCE(source_protocol,'')<>? OR COALESCE(source_container,'')<>? OR COALESCE(additional_parts,'null')<>?)`,
		entry.Size, mtime, m.SourceProtocol, m.SourceContainer, parts, time.Now().UTC().Format(time.RFC3339), id,
		entry.Size, mtime, m.SourceProtocol, m.SourceContainer, parts)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// preserveIndexedMetadataTx follows the file for a row whose sidecar exists but
// cannot be parsed. Structure columns move with the source; metadata, actors,
// features and status stay exactly as indexed, so the row stays visible and a
// repaired NFO can refresh it later.
func preserveIndexedMetadataTx(tx *sql.Tx, id int64, entry ScanGroupMovie) (bool, error) {
	changed, err := updateManualScanStructureTx(tx, id, entry)
	if err != nil {
		return false, err
	}
	return changed, setScanNoteTx(tx, id, entry.MetadataError)
}

// setScanNoteTx records why a scan pass could not refresh metadata. The note
// shares last_scrape_error with scrape outcomes but keeps its own prefix.
func setScanNoteTx(tx *sql.Tx, id int64, note string) error {
	if note == "" {
		return nil
	}
	_, err := tx.Exec(`UPDATE movies SET last_scrape_error=? WHERE id=? AND COALESCE(last_scrape_error,'')<>?`, note, id, note)
	return err
}

// clearScanNoteTx drops a note left by an earlier scan pass once the sidecar
// parses again. Scrape failures and operator confirmations use the same column
// but different prefixes, so they survive.
func clearScanNoteTx(tx *sql.Tx, id int64) error {
	_, err := tx.Exec(`UPDATE movies SET last_scrape_error='' WHERE id=? AND last_scrape_error LIKE ?`, id, ScanNFONotePrefix+"%")
	return err
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// ScanReconcileResult reports rows removed by a complete inventory pass.
type ScanReconcileResult struct {
	DeletedMovies int
	DeletedGroups int
}

// ReconcileScanInventory removes movie rows that the complete group inventory
// no longer expects, and stale group baselines. Callers must have proved the
// inventory complete; expected maps every primary source path to its committed
// movie ID, including preserved manual parts. A zero-change pass obtains a
// writer reservation but performs no row changes and no version bump.
func (s *Store) ReconcileScanInventory(libraryID int64, expected map[string]int64, groupKeys []string) (ScanReconcileResult, error) {
	return s.reconcileScanInventory(context.Background(), Library{ID: libraryID}, false, expected, groupKeys)
}

func (s *Store) ReconcileScanInventoryChecked(lib Library, expected map[string]int64, groupKeys []string) (ScanReconcileResult, error) {
	return s.ReconcileScanInventoryCheckedContext(context.Background(), lib, expected, groupKeys)
}

func (s *Store) ReconcileScanInventoryCheckedContext(ctx context.Context, lib Library, expected map[string]int64, groupKeys []string) (ScanReconcileResult, error) {
	return s.reconcileScanInventory(ctx, lib, true, expected, groupKeys)
}

func (s *Store) reconcileScanInventory(ctx context.Context, lib Library, checkPath bool, expected map[string]int64, groupKeys []string) (ScanReconcileResult, error) {
	libraryID := lib.ID
	if expected == nil {
		return ScanReconcileResult{}, ErrScanInventoryChanged
	}
	for path, id := range expected {
		if path == "" || id <= 0 {
			return ScanReconcileResult{}, ErrScanInventoryChanged
		}
	}
	keepGroups := make(map[string]bool, len(groupKeys))
	for _, key := range groupKeys {
		keepGroups[key] = true
	}
	result := ScanReconcileResult{}
	var version int64
	err := s.writeTx(func(tx *sql.Tx) error {
		result = ScanReconcileResult{}
		version = 0
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := reserveScanWriterTxContext(ctx, tx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateScanLibraryTx(tx, lib, checkPath); err != nil {
			return err
		}
		staleGroups := make([]string, 0)
		rows, err := tx.Query(`SELECT group_key FROM scan_groups WHERE library_id = ?`, libraryID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			if !keepGroups[key] {
				staleGroups = append(staleGroups, key)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, key := range staleGroups {
			if err := ctx.Err(); err != nil {
				return err
			}
			res, err := tx.Exec(`DELETE FROM scan_groups WHERE library_id = ? AND group_key = ?`, libraryID, key)
			if err != nil {
				return err
			}
			removed, err := res.RowsAffected()
			if err != nil {
				return err
			}
			result.DeletedGroups += int(removed)
		}

		var deleteIDs []int64
		var deletePaths []string
		matchedExpected := 0
		rows, err = tx.Query(`SELECT id, source_path FROM movies WHERE library_id = ?`, libraryID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var path string
			if err := rows.Scan(&id, &path); err != nil {
				rows.Close()
				return err
			}
			if expectedID, ok := expected[path]; !ok {
				deleteIDs = append(deleteIDs, id)
				deletePaths = append(deletePaths, path)
			} else if expectedID != id {
				rows.Close()
				return ErrScanInventoryChanged
			} else {
				matchedExpected++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if matchedExpected != len(expected) {
			return ErrScanInventoryChanged
		}
		if err := InvalidateScanGroupsForPathsTx(tx, libraryID, deletePaths); err != nil {
			return err
		}
		for start := 0; start < len(deleteIDs); start += 500 {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := min(start+500, len(deleteIDs))
			batch := deleteIDs[start:end]
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
			args := make([]any, 0, len(batch))
			for _, id := range batch {
				args = append(args, id)
			}
			if _, err := tx.Exec(`DELETE FROM movies WHERE id IN (`+placeholders+`)`, args...); err != nil {
				return err
			}
		}
		result.DeletedMovies = len(deleteIDs)
		if result.DeletedMovies > 0 {
			var err error
			version, err = bumpVersionTx(tx, libraryID)
			if err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return ScanReconcileResult{}, err
	}
	if version > 0 {
		s.publishVersion(version)
	}
	return result, nil
}

// InvalidateScanGroupsForPathsTx drops baselines that depend on any changed
// source path. The scanner's own commit writes its replacement baseline in the
// same transaction, after this helper has retired overlapping old baselines.
func InvalidateScanGroupsForPathsTx(tx *sql.Tx, libraryID int64, paths []string) error {
	seen := make(map[string]bool, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		unique = append(unique, path)
	}
	if len(unique) == 0 {
		return nil
	}
	for start := 0; start < len(unique); start += 400 {
		end := min(start+400, len(unique))
		batch := unique[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch)+2)
		args = append(args, libraryID, libraryID)
		for _, path := range batch {
			args = append(args, path)
		}
		if _, err := tx.Exec(`DELETE FROM scan_groups WHERE library_id = ? AND group_key IN (
			SELECT group_key FROM scan_group_sources WHERE library_id = ? AND source_path IN (`+placeholders+`))`, args...); err != nil {
			return err
		}
	}
	return nil
}

// LibraryMovieCount returns how many indexed movies a library currently has.
// The scanner uses it to distinguish a legitimately empty library from a
// missing mount without relying on settings or filesystem identity.
func (s *Store) LibraryMovieCount(libraryID int64) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM movies WHERE library_id = ?`, libraryID).Scan(&count)
	return count, err
}

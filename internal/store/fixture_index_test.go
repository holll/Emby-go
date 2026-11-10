package store

import (
	"context"
	"time"
)

// Snapshot mutations and SQL fixture setup both return a value that older
// error-only test tables do not inspect. Keep that adapter out of production.
func snapshotError(_ any, err error) error { return err }

// Actor fixtures are indexed through the production movie transaction, retaining
// the row's source signature rather than restoring the removed actor-only writer.
func indexFixtureActors(s *Store, id int64, actors []ActorRef) error {
	movie, err := s.Movie(id)
	if err != nil {
		return err
	}
	var size int64
	var stamp string
	if err := s.db.QueryRow("SELECT file_size, file_mtime FROM movies WHERE id=?", id).Scan(&size, &stamp); err != nil {
		return err
	}
	mtime, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return err
	}
	_, err = s.IndexMovie(movie, size, mtime, actors)
	return err
}

// Build the explicit evidence required by the current scanner cleanup API from
// the retained fixture paths. All reconciliation and CAS stay in production.
func reconcileFixtureInventory(s *Store, libraryID int64, paths map[string]struct{}) error {
	lib, err := s.Library(libraryID)
	if err != nil {
		return err
	}
	evidence, err := s.LoadScanIndexEvidence(libraryID)
	if err != nil {
		return err
	}
	expected := make(map[string]int64, len(paths))
	for path := range paths {
		expected[path] = evidence[path].ID
	}
	_, err = s.ReconcileScanInventoryCheckedContext(context.Background(), lib, expected, nil)
	return err
}

// TryLock exists only as an observation probe for writer-admission tests.
func (g *writerGate) TryLock() bool {
	g.init()
	select {
	case g.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

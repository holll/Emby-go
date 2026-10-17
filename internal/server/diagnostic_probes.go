package server

// Lock order: persistMu -> probeMu. Reservation in noRoute releases probeMu
// before waiting for persistMu; duplicate requests never wait for SQLite.
func (a *App) persistDiagnosticProbe(method, path, query string, epoch uint64) {
	a.probePersistMu.Lock()
	defer a.probePersistMu.Unlock()
	a.probeMu.Lock()
	current := epoch == a.probeEpoch
	a.probeMu.Unlock()
	if !current {
		// A clear committed after this request reserved its slot. Neither write the
		// old observation nor delete a replacement reservation from the new epoch.
		return
	}
	if err := a.db.Probe(method, path, query, "[OMITTED]"); err != nil {
		a.probeMu.Lock()
		delete(a.probeSeen, method+" "+path)
		a.probeMu.Unlock()
	}
}

func (a *App) clearDiagnosticProbes() error {
	a.probePersistMu.Lock()
	defer a.probePersistMu.Unlock()
	if err := a.db.ClearProbes(); err != nil {
		return err
	}
	a.probeMu.Lock()
	a.probeSeen = make(map[string]struct{})
	a.probeEpoch++
	a.probeMu.Unlock()
	return nil
}

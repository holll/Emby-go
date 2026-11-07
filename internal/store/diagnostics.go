package store

// SchemaVersion 返回迁移账本中的最大版本号，供诊断页核对实际库结构。
func (s *Store) SchemaVersion() (int, error) {
	var v int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// TaskRecordCount 返回保留中的任务摘要条数。
func (s *Store) TaskRecordCount() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_records`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

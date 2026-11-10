package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
)

// N-02：扫描/后台任务摘要落库。只保留下有界的历史摘要，不存执行器内部状态，
// 重启后不恢复、不补跑；未完成的行标记为 interrupted。
const taskRecordsMigrationVersion = 19

const migration19Statement = `CREATE TABLE IF NOT EXISTS task_records (
	id INTEGER PRIMARY KEY,
	type TEXT NOT NULL,
	status TEXT NOT NULL,
	library_id INTEGER NOT NULL DEFAULT 0,
	mode TEXT NOT NULL DEFAULT '',
	allow_empty INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	started_at TEXT NOT NULL DEFAULT '',
	finished_at TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	result_json TEXT NOT NULL DEFAULT ''
)`

func migration19Checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(migration19Statement)))
}

func applyMigration19(tx *sql.Tx) error {
	if _, err := tx.Exec(migration19Statement); err != nil {
		return fmt.Errorf("schema migration %d: %w", taskRecordsMigrationVersion, err)
	}
	return nil
}

// TaskRecord 是一行任务摘要。ResultJSON 仅扫描类任务写入。
type TaskRecord struct {
	ID         int64
	Type       string
	Status     string
	LibraryID  int64
	Mode       string
	AllowEmpty bool
	CreatedAt  string
	StartedAt  string
	FinishedAt string
	Error      string
	ResultJSON string
}

const taskRecordRetention = 500

// UpsertTaskRecord 以调用方分配的 id 写入或更新一行摘要。扫描入口在返回 202
// 之前调用它，保证「已受理」的任务在重启前后都可查询。
func (s *Store) UpsertTaskRecord(rec TaskRecord) error {
	allowEmpty := 0
	if rec.AllowEmpty {
		allowEmpty = 1
	}
	_, err := s.db.Exec(`INSERT INTO task_records(id,type,status,library_id,mode,allow_empty,created_at,started_at,finished_at,error,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET type=excluded.type,status=excluded.status,library_id=excluded.library_id,
		mode=excluded.mode,allow_empty=excluded.allow_empty,created_at=excluded.created_at,
		started_at=excluded.started_at,finished_at=excluded.finished_at,error=excluded.error,result_json=excluded.result_json`,
		rec.ID, rec.Type, rec.Status, rec.LibraryID, rec.Mode, allowEmpty, rec.CreatedAt, rec.StartedAt, rec.FinishedAt, rec.Error, rec.ResultJSON)
	if err != nil {
		return fmt.Errorf("upsert task record: %w", err)
	}
	return s.PruneTaskRecords(taskRecordRetention)
}

// FinishTaskRecord 记录终态；失败只记在摘要里，不改其它任务行。
func (s *Store) FinishTaskRecord(id int64, status, finishedAt, errMsg, resultJSON string) error {
	_, err := s.db.Exec(`UPDATE task_records SET status=?,finished_at=?,error=?,result_json=? WHERE id=?`,
		status, finishedAt, errMsg, resultJSON, id)
	if err != nil {
		return fmt.Errorf("finish task record: %w", err)
	}
	return nil
}

// MarkInterruptedTaskRecords 在启动时把上次进程遗留的 queued/running 行标成
// interrupted。只做一次、无重试：进程已重启，这些任务不会再被执行。
func (s *Store) MarkInterruptedTaskRecords(finishedAt string) (int64, error) {
	res, err := s.db.Exec(`UPDATE task_records SET status='interrupted',finished_at=?,
		error=CASE WHEN error='' THEN '进程重启，任务执行状态丢失' ELSE error END
		WHERE status IN ('queued','running')`, finishedAt)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted task records: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark interrupted task records: %w", err)
	}
	return n, nil
}

func (s *Store) ListTaskRecords(limit int) ([]TaskRecord, error) {
	rows, err := s.db.Query(`SELECT id,type,status,library_id,mode,allow_empty,created_at,started_at,finished_at,error,result_json
		FROM task_records ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list task records: %w", err)
	}
	defer rows.Close()
	var out []TaskRecord
	for rows.Next() {
		var rec TaskRecord
		var allowEmpty int
		if err := rows.Scan(&rec.ID, &rec.Type, &rec.Status, &rec.LibraryID, &rec.Mode, &allowEmpty,
			&rec.CreatedAt, &rec.StartedAt, &rec.FinishedAt, &rec.Error, &rec.ResultJSON); err != nil {
			return nil, err
		}
		rec.AllowEmpty = allowEmpty != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) TaskRecordByID(id int64) (TaskRecord, bool, error) {
	var rec TaskRecord
	var allowEmpty int
	err := s.db.QueryRow(`SELECT id,type,status,library_id,mode,allow_empty,created_at,started_at,finished_at,error,result_json
		FROM task_records WHERE id=?`, id).
		Scan(&rec.ID, &rec.Type, &rec.Status, &rec.LibraryID, &rec.Mode, &allowEmpty,
			&rec.CreatedAt, &rec.StartedAt, &rec.FinishedAt, &rec.Error, &rec.ResultJSON)
	if err == sql.ErrNoRows {
		return TaskRecord{}, false, nil
	}
	if err != nil {
		return TaskRecord{}, false, fmt.Errorf("task record by id: %w", err)
	}
	rec.AllowEmpty = allowEmpty != 0
	return rec, true, nil
}

func (s *Store) MaxTaskRecordID() (int64, error) {
	var id int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM task_records`).Scan(&id); err != nil {
		return 0, fmt.Errorf("max task record id: %w", err)
	}
	return id, nil
}

func (s *Store) PruneTaskRecords(keep int) error {
	_, err := s.db.Exec(`DELETE FROM task_records WHERE id NOT IN (SELECT id FROM task_records ORDER BY id DESC LIMIT ?)`, keep)
	if err != nil {
		return fmt.Errorf("prune task records: %w", err)
	}
	return nil
}

// SetTaskRecordStarted 记录任务真正开始执行的时间；登记阶段仍为 queued。
func (s *Store) SetTaskRecordStarted(id int64, startedAt string) error {
	_, err := s.db.Exec(`UPDATE task_records SET status='running',started_at=? WHERE id=?`, startedAt, id)
	if err != nil {
		return fmt.Errorf("start task record: %w", err)
	}
	return nil
}

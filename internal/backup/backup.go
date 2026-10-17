// Package backup 实现 D11 的每日备份契约：SQLite 一致性快照 + 媒体库 NFO 副本，
// 保留最近 N 份并可按时间倒序清理。备份只读源库，不修改业务数据。
package backup

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Options 控制一次备份。
type Options struct {
	DBPath  string    // 源数据库
	OutDir  string    // 备份根目录
	Retain  int       // 保留份数（<=0 表示不清理）
	Now     time.Time // 备份时间（零值用 time.Now）
	NFORoot bool      // 是否复制媒体库目录中的 .nfo
}

// Result 描述本次备份产物，便于日志与测试断言。
type Result struct {
	Dir        string
	Database   string
	NFOCount   int
	Pruned     []string
	FinishedAt time.Time
}

// Run 执行一次备份。目录名使用 UTC 时间戳，重复执行会覆盖同名目录。
func Run(opts Options) (Result, error) {
	if strings.TrimSpace(opts.DBPath) == "" || strings.TrimSpace(opts.OutDir) == "" {
		return Result{}, fmt.Errorf("backup: db and out dir required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	dir := filepath.Join(opts.OutDir, now.UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("backup: %w", err)
	}
	db, err := sql.Open("sqlite", opts.DBPath)
	if err != nil {
		return Result{}, fmt.Errorf("backup: open db: %w", err)
	}
	defer db.Close()
	dest := filepath.Join(dir, "db.sqlite")
	if _, err := db.Exec("VACUUM INTO ?", dest); err != nil {
		return Result{}, fmt.Errorf("backup: snapshot: %w", err)
	}
	result := Result{Dir: dir, Database: dest, FinishedAt: now.UTC()}
	if opts.NFORoot {
		count, err := copyNFO(db, filepath.Join(dir, "nfo"))
		if err != nil {
			return Result{}, err
		}
		result.NFOCount = count
	}
	if opts.Retain > 0 {
		pruned, err := prune(opts.OutDir, opts.Retain)
		if err != nil {
			return Result{}, err
		}
		result.Pruned = pruned
	}
	return result, nil
}

// copyNFO 按库复制 .nfo 文件，保持库内相对路径；任何读写错误都让备份失败，
// 避免出现"数据库有、NFO 没有"的静默半份备份。
func copyNFO(db *sql.DB, root string) (int, error) {
	rows, err := db.Query("SELECT id,path FROM libraries ORDER BY id")
	if err != nil {
		return 0, fmt.Errorf("backup: libraries: %w", err)
	}
	defer rows.Close()
	type library struct {
		id   int64
		path string
	}
	var libs []library
	for rows.Next() {
		var l library
		if err := rows.Scan(&l.id, &l.path); err != nil {
			return 0, fmt.Errorf("backup: libraries: %w", err)
		}
		libs = append(libs, l)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("backup: libraries: %w", err)
	}
	count := 0
	for _, lib := range libs {
		info, err := os.Stat(lib.path)
		if err != nil || !info.IsDir() {
			continue // 离线/移除的库不阻断备份，恢复时按 NFO 目录人工对账
		}
		err = filepath.Walk(lib.path, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".nfo") {
				return nil
			}
			rel, err := filepath.Rel(lib.path, path)
			if err != nil {
				return err
			}
			dst := filepath.Join(root, fmt.Sprintf("lib-%d", lib.id), rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := copyFile(path, dst); err != nil {
				return err
			}
			count++
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("backup: copy nfo: %w", err)
		}
	}
	return count, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// prune 只清理符合备份时间戳命名的目录，保留最新 retain 份。
func prune(outDir string, retain int) ([]string, error) {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, fmt.Errorf("backup: prune: %w", err)
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := time.Parse("20060102T150405Z", entry.Name()); err != nil {
			continue
		}
		dirs = append(dirs, entry.Name())
	}
	sort.Strings(dirs)
	if len(dirs) <= retain {
		return nil, nil
	}
	var pruned []string
	for _, name := range dirs[:len(dirs)-retain] {
		if err := os.RemoveAll(filepath.Join(outDir, name)); err != nil {
			return pruned, fmt.Errorf("backup: prune %s: %w", name, err)
		}
		pruned = append(pruned, name)
	}
	return pruned, nil
}

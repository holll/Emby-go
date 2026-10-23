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
	// 所有会删除、创建或遍历的路径在写文件前校验。固定本次使用的库清单，
	// 避免校验后再从活动库读取另一组路径。
	// 固定规范化路径供校验、发布和清理共同使用，不能让 prune 再解释原始路径。
	var err error
	opts.OutDir, err = canonicalPath(opts.OutDir)
	if err != nil {
		return Result{}, fmt.Errorf("backup: resolve output: %w", err)
	}
	opts.DBPath, err = canonicalPath(opts.DBPath)
	if err != nil {
		return Result{}, fmt.Errorf("backup: resolve database: %w", err)
	}
	db, err := sql.Open("sqlite", readonlyDSN(opts.DBPath))
	if err != nil {
		return Result{}, fmt.Errorf("backup: open db: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	libs, err := readLibraries(db, opts.NFORoot)
	if err != nil {
		return Result{}, err
	}
	if err := validateOutputPaths(opts.DBPath, opts.OutDir, libs); err != nil {
		return Result{}, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	dir := filepath.Join(opts.OutDir, now.UTC().Format("20060102T150405Z"))
	// 先在隐藏暂存目录写完，成功后再改名：失败或崩溃不会留下半份备份，
	// 也不会覆盖同名时间戳下已有的完整备份。
	stage := filepath.Join(opts.OutDir, "."+now.UTC().Format("20060102T150405Z")+".tmp")
	if err := os.RemoveAll(stage); err != nil {
		return Result{}, fmt.Errorf("backup: clean staging: %w", err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return Result{}, fmt.Errorf("backup: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(stage)
		}
	}()
	// 源库只读，busy_timeout 给在线写入者让路。
	dest := filepath.Join(stage, "db.sqlite")
	if _, err := db.Exec("VACUUM INTO ?", dest); err != nil {
		return Result{}, fmt.Errorf("backup: snapshot: %w", err)
	}
	if err := checkSnapshot(dest); err != nil {
		return Result{}, err
	}
	result := Result{Dir: dir, Database: filepath.Join(dir, "db.sqlite"), FinishedAt: now.UTC()}
	if opts.NFORoot {
		count, err := copyNFO(libs, filepath.Join(stage, "nfo"))
		if err != nil {
			return Result{}, err
		}
		result.NFOCount = count
	}
	if err := os.RemoveAll(dir); err != nil {
		return Result{}, fmt.Errorf("backup: replace: %w", err)
	}
	if err := os.Rename(stage, dir); err != nil {
		return Result{}, fmt.Errorf("backup: publish: %w", err)
	}
	committed = true
	// 先发布再清理旧份，保留策略才会把本次备份计入份数。
	if opts.Retain > 0 {
		pruned, err := prune(opts.OutDir, opts.Retain)
		if err != nil {
			return Result{}, err
		}
		result.Pruned = pruned
	}
	return result, nil
}

// validateOutputPaths 要求备份根目录与源文件/媒体库分离。除了防止库内备份
// 复制自身，也防止 replace/prune 把恰好使用时间戳目录名的源库删掉。
func validateOutputPaths(dbPath, outDir string, libs []backupLibrary) error {
	output, err := canonicalPath(outDir)
	if err != nil {
		return fmt.Errorf("backup: resolve output: %w", err)
	}
	database, err := canonicalPath(dbPath)
	if err != nil {
		return fmt.Errorf("backup: resolve database: %w", err)
	}
	if containsPath(output, database) || containsPath(database, output) {
		return fmt.Errorf("backup: output directory overlaps source database")
	}
	for _, lib := range libs {
		root, err := canonicalPath(lib.path)
		if err != nil {
			return fmt.Errorf("backup: resolve library %d: %w", lib.id, err)
		}
		if containsPath(root, output) || containsPath(output, root) {
			return fmt.Errorf("backup: output directory overlaps library %d", lib.id)
		}
	}
	return nil
}

func containsPath(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonicalPath 解析已有前缀的符号链接，同时支持尚未创建的输出/离线库。
// 悬空链接不可当成普通不存在路径处理，否则可能绕过重叠校验。
func canonicalPath(path string) (string, error) {
	current := path
	if !filepath.IsAbs(current) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// 不使用 Join/Abs：必须先跟随符号链接，再处理其后的 ..。
		current = cwd + string(filepath.Separator) + current
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if _, statErr := os.Lstat(current); statErr == nil || !os.IsNotExist(statErr) {
			return "", err
		}
		dir, base := filepath.Split(current)
		parent := strings.TrimSuffix(dir, string(filepath.Separator))
		if parent == filepath.VolumeName(parent) {
			parent = dir // 保留 / 或盘符根目录。
		}
		if parent == current || parent == "" {
			return "", err
		}
		suffix = append(suffix, base)
		current = parent
	}
}

// readonlyDSN 只开放读事务，并用 busy_timeout 等待在线写入者。
func readonlyDSN(path string) string {
	return "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
}

// checkSnapshot 校验快照完整性；半成品或损坏的备份不允许发布。
func checkSnapshot(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("backup: verify snapshot: %w", err)
	}
	defer db.Close()
	rows, err := db.Query("PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("backup: verify snapshot: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var verdict string
		if err := rows.Scan(&verdict); err != nil {
			return fmt.Errorf("backup: verify snapshot: %w", err)
		}
		if verdict != "ok" {
			return fmt.Errorf("backup: integrity check failed: %s", verdict)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("backup: verify snapshot: %w", err)
	}
	return nil
}

type backupLibrary struct {
	id   int64
	path string
}

func readLibraries(db *sql.DB, required bool) ([]backupLibrary, error) {
	if !required {
		// 兼容只含业务自定义表的数据库备份，但应用库即使不复制 NFO
		// 也必须保护媒体目录不被 replace/prune 清理。
		var exists bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='libraries')").Scan(&exists); err != nil {
			return nil, fmt.Errorf("backup: libraries schema: %w", err)
		}
		if !exists {
			return nil, nil
		}
	}
	rows, err := db.Query("SELECT id,path FROM libraries ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("backup: libraries: %w", err)
	}
	defer rows.Close()
	var libs []backupLibrary
	for rows.Next() {
		var l backupLibrary
		if err := rows.Scan(&l.id, &l.path); err != nil {
			return nil, fmt.Errorf("backup: libraries: %w", err)
		}
		libs = append(libs, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: libraries: %w", err)
	}
	return libs, nil
}

// copyNFO 按库复制 .nfo 文件，保持库内相对路径；任何读写错误都让备份失败，
// 避免出现"数据库有、NFO 没有"的静默半份备份。
func copyNFO(libs []backupLibrary, root string) (int, error) {
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
	if err := out.Sync(); err != nil {
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

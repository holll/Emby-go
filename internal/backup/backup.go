// Package backup 实现 D11 的每日备份契约：SQLite 一致性快照 + 媒体库 NFO 副本，
// 保留最近 N 份并可按时间倒序清理。备份只读源库，不修改业务数据。
package backup

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
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

// lockFileName 是输出目录上的互斥锁文件：同一 OutDir 同一时刻只允许一路备份。
const lockFileName = ".embybackup.lock"

// ErrOutputBusy 表示该输出目录已有另一路备份在进行（另一个进程或同进程另一路）。
var ErrOutputBusy = errors.New("backup: 该输出目录已有备份正在进行")

// lockStaleAfter 是「无法判断持锁者死活」时的陈旧锁兜底阈值。
// 备份正常只需秒级，所以超过这个时长仍未释放的锁（写不全的锁文件、跨平台无法
// 探测 pid、持锁 pid 已被复用等）可以安全接管，避免人工清理现场。
const lockStaleAfter = 30 * time.Minute

// acquireOutputLock 在输出目录上取互斥锁（O_CREATE|O_EXCL），返回释放函数。
//
// 为什么不只靠「stage 名加随机后缀」：随机后缀能防住 stage 撞车，但两路备份仍会
// 各自 VACUUM、各自 os.RemoveAll(dir)、各自 rename 到同一个最终目录，后完成的把
// 先完成的那份删掉；prune 也会被两路并发跑。所以同一输出目录同一时刻只允许一路。
//
// 陈旧锁策略：锁文件里写 pid + 启动时间。
//  1. 持锁进程还活着 → 直接 ErrOutputBusy（无论锁多久，长备份是合法的）；
//  2. 进程已确认不存在（kill -9、容器重启）→ 立即接管；
//  3. 拿不到结论（锁文件被写坏、平台不支持信号 0、pid 已被别的进程复用）→
//     按锁文件 mtime 判断，超过 lockStaleAfter 才接管，宁可等也不并发写。
func acquireOutputLock(outDir string) (func(), error) {
	path := filepath.Join(outDir, lockFileName)
	file, err := createLockFile(path)
	if errors.Is(err, os.ErrExist) {
		if staleErr := dropStaleLock(path); staleErr != nil {
			return nil, staleErr
		}
		file, err = createLockFile(path)
		if errors.Is(err, os.ErrExist) {
			// 两路同时判定「陈旧锁」并接管，O_EXCL 保证只有一路建成；
			// 另一路按「有人持锁」报，而不是抛一个裸的 file exists。
			return nil, fmt.Errorf("%w（%s）", ErrOutputBusy, path)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("backup: acquire lock: %w", err)
	}
	// 记录持锁者身份，供下一位（包括被 kill 后的下一次启动）判断可否接管。
	if _, err := fmt.Fprintf(file, "pid=%d\nstart=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("backup: write lock: %w", err)
	}
	return func() {
		_ = file.Close()
		_ = os.Remove(path)
	}, nil
}

func createLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
}

// dropStaleLock 决定已存在的锁能否接管：能接管就删掉锁文件（由调用方重新 O_EXCL
// 创建，两路同时接管时必然只有一路成功），不能接管就返回 ErrOutputBusy。
func dropStaleLock(path string) error {
	info, statErr := os.Stat(path)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil // 对方刚好释放，直接重试创建
		}
		return fmt.Errorf("backup: inspect lock: %w", statErr)
	}
	pid := readLockPID(path)
	alive, known := processAlive(pid)
	if known && alive {
		return fmt.Errorf("%w（%s，pid=%d）", ErrOutputBusy, path, pid)
	}
	if !known && time.Since(info.ModTime()) < lockStaleAfter {
		// 拿不到结论就别抢：锁文件可能是别的机器上的合法备份（共享输出目录）。
		return fmt.Errorf("%w（%s，无法确认持锁者存活）", ErrOutputBusy, path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup: take over stale lock: %w", err)
	}
	return nil
}

// readLockPID 读出锁文件里的 pid；文件写不全或被改坏时返回 0（视为未知）。
func readLockPID(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "pid=")
		if !ok {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// processAlive 判断 pid 对应的进程是否还在。known=false 表示本平台/本权限下
// 无法下结论（调用方应改用时间兜底，而不是猜）。
func processAlive(pid int) (alive, known bool) {
	if pid <= 0 {
		return false, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, true
	}
	// 信号 0 只做存在性检查，不投递任何信号。
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true, true
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, true
	}
	// EPERM 说明进程存在但不属于我们（同一输出目录被别的用户用着）→ 视为存活，
	// 其它错误一律不下结论。
	if errors.Is(err, syscall.EPERM) {
		return true, true
	}
	return false, false
}

// 备份流程内的测试同步点：生产路径恒为 nil，开销只是一次 nil 判断。
// 「两路同秒备份互相删掉对方 stage」只在「A 已写完快照、还没发布」这个窗口里
// 出现，靠 sleep 碰时序既不可靠又慢，所以留钩子让测试精确插同步点。
var (
	testHookBeforeSnapshot func() // VACUUM 之前（此前已 MkdirAll 并清理过 stage）
	testHookAfterSnapshot  func() // 快照校验通过之后、发布之前
)

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
	// 输出目录上的互斥锁要在任何 mkdir/remove 之前拿到：stage 与最终目录都派生自
	// 同一个秒级时间戳，两路同秒启动会共用一个 stage，后启者的 os.RemoveAll(stage)
	// 会删掉先启者正在 VACUUM 的库，先启者随后把半成品 rename 成「完整备份」。
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("backup: output dir: %w", err)
	}
	releaseLock, err := acquireOutputLock(opts.OutDir)
	if err != nil {
		return Result{}, err
	}
	defer releaseLock()
	// 先在隐藏暂存目录写完，成功后再改名：失败或崩溃不会留下半份备份，
	// 也不会覆盖同名时间戳下已有的完整备份。
	// stage 名字带随机后缀：不再与秒级时间戳一一对应，任何人（包括没拿到锁的旧版本、
	// 手工清理、崩溃残留）都不可能与本路落到同一个目录上再互相删除。
	stage := filepath.Join(opts.OutDir, "."+now.UTC().Format("20060102T150405Z")+"."+rand.Text()+".tmp")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return Result{}, fmt.Errorf("backup: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(stage)
		}
	}()
	if testHookBeforeSnapshot != nil {
		testHookBeforeSnapshot()
	}
	// 源库只读，busy_timeout 给在线写入者让路。
	dest := filepath.Join(stage, "db.sqlite")
	if _, err := db.Exec("VACUUM INTO ?", dest); err != nil {
		return Result{}, fmt.Errorf("backup: snapshot: %w", err)
	}
	if err := checkSnapshot(dest); err != nil {
		return Result{}, err
	}
	if testHookAfterSnapshot != nil {
		testHookAfterSnapshot()
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
			for _, s := range slices.Backward(suffix) {
				resolved = filepath.Join(resolved, s)
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

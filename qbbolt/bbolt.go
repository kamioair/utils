// Package qbbolt 封装 bbolt，提供按时间分文件的 KV 存储。
//
// 这是标准库：其他模块可以直接 import 使用。
//
// 设计要点：
//   - 按 day 或 month 粒度拆分多个 bbolt 文件，按 YYYY/MM 分目录
//   - 每个文件只有一个 bucket "data"
//   - 按天存储时 key 剥除日期前缀
//   - "current 句柄" 优化：写命中时复用
//   - **健壮性**：文件损坏/丢失时自愈（IVD 场景）
//   - Get：文件损坏/丢失/读失败 → 返回明确错误，不崩溃
//   - Put：文件损坏/丢失 → log + 自动删旧建新 + 重试，不返回错误
//   - 可选 ErrorHandler：外部订阅事件（自愈、损坏、错误）用于审计
package qbbolt

import (
	"errors"        // 错误类型
	"fmt"           // 错误格式化
	"io/fs"         // fs.ErrNotExist
	"log"           // 自愈等关键事件记录
	"os"            // 文件操作
	"path/filepath" // 路径拼接
	"sync"          // current 句柄互斥
	"time"          // 时间戳

	bolt "go.etcd.io/bbolt"
)

const dataBucket = "data"

// StoreConfig 存储层配置
type StoreConfig struct {
	Granularity   string // "day" | "month"
	Compress      bool
	CompressAlgo  string // "zstd" | "gzip"
	CompressLevel int
	SyncWrites    bool
}

// 公开错误：调用者用 errors.Is 区分
var (
	// ErrNotFound key 不存在
	ErrNotFound = errors.New("bboltstore: key not found")
	// ErrFileMissing 文件不存在（被删或从未写过）
	ErrFileMissing = errors.New("bboltstore: file missing")
	// ErrFileCorrupt 文件存在但 bbolt 打开失败（CRC 错误、元数据损坏等）
	ErrFileCorrupt = errors.New("bboltstore: file corrupt")
)

// ErrorEvent 错误/自愈事件，供外部订阅（如 IVD 的审计日志）
type ErrorEvent struct {
	Op     string // "open" | "get" | "put"
	Path   string // 出错的文件路径
	Err    error  // 底层错误（仅日志展示用，调用方不应 swallow）
	Action string // "return_error" | "self_heal_started" | "self_heal_succeeded" | "self_heal_failed"
}

// Store 接口
type Store interface {
	Open(baseDir, prefix string, cfg StoreConfig) error
	Close() error
	Put(ts time.Time, key []byte, value []byte) error
	Get(ts time.Time, key []byte) ([]byte, error)
	ListBucket(ts time.Time) (map[string][]byte, error)
	FileSize() int64
	FileCount() int

	// SetErrorHandler 设置错误回调（用于 IVD 审计日志）
	// 不设置时，文件损坏仍会 log 到 stderr，但不会调用任何回调
	SetErrorHandler(h func(ErrorEvent))
}

// NewBoltStore 构造函数
func NewBoltStore(baseDir, prefix string, cfg StoreConfig) (Store, error) {
	s := &BoltStore{baseDir: baseDir, prefix: prefix, cfg: cfg}
	if err := s.Open(baseDir, prefix, cfg); err != nil {
		return nil, err
	}
	return s, nil
}

// BoltStore bbolt + current 句柄 + 自愈 + ErrorHandler
type BoltStore struct {
	baseDir string
	prefix  string
	cfg     StoreConfig
	cmpr    Compressor

	mu          sync.Mutex
	currentName string
	currentDB   *bolt.DB

	// 错误回调（外部注入，IVD 审计用）
	handlerMu    sync.RWMutex
	errorHandler func(ErrorEvent)
}

func (s *BoltStore) Open(baseDir, prefix string, cfg StoreConfig) error {
	s.baseDir = baseDir
	s.prefix = prefix
	s.cfg = cfg
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return fmt.Errorf("create base dir: %w", err)
	}
	if cfg.Compress {
		var err error
		s.cmpr, err = NewCompressor(cfg.CompressAlgo, cfg.CompressLevel)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *BoltStore) Close() error {
	if s.cmpr != nil {
		_ = s.cmpr.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentDB != nil {
		err := s.currentDB.Close()
		s.currentDB = nil
		s.currentName = ""
		return err
	}
	return nil
}

// SetErrorHandler 设置错误事件回调。传 nil 取消订阅。
func (s *BoltStore) SetErrorHandler(h func(ErrorEvent)) {
	s.handlerMu.Lock()
	s.errorHandler = h
	s.handlerMu.Unlock()
}

// notify 调用错误回调（如果已设置）。无锁拷贝函数指针，避免回调里再调 SetXxx 死锁。
func (s *BoltStore) notify(event ErrorEvent) {
	s.handlerMu.RLock()
	h := s.errorHandler
	s.handlerMu.RUnlock()
	if h != nil {
		h(event)
	}
}

// filePath 返回 db 文件完整路径
func (s *BoltStore) filePath(ts time.Time) string {
	if s.cfg.Granularity == "month" {
		return filepath.Join(s.baseDir, ts.Format("2006"),
			fmt.Sprintf("%s-%s.db", s.prefix, ts.Format("200601")))
	}
	return filepath.Join(s.baseDir, ts.Format("2006"), ts.Format("01"),
		fmt.Sprintf("%s-%s.db", s.prefix, ts.Format("20060102")))
}

// fileName（保持兼容，等同 filePath）
func (s *BoltStore) fileName(ts time.Time) string { return s.filePath(ts) }

// datePrefix 返回日期前缀
func (s *BoltStore) datePrefix(ts time.Time) string {
	if s.cfg.Granularity == "month" {
		return ts.Format("200601")
	}
	return ts.Format("20060102")
}

// encodeKey 按 granularity 编码 key
func (s *BoltStore) encodeKey(ts time.Time, key []byte) []byte {
	if s.cfg.Granularity == "day" {
		prefix := s.datePrefix(ts)
		if len(key) > len(prefix) && string(key[:len(prefix)]) == prefix {
			return key[len(prefix):]
		}
		return key
	}
	return key
}

// openAndOpen 打开文件并返回错误分类后的错误
// 用于 Get/ListBucket：不维护 current，错误时直接返回
func (s *BoltStore) openAndRead(ts time.Time, fn func(*bolt.DB) error) error {
	path := s.filePath(ts)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create subdir: %w", err)
	}
	// 预检查文件是否存在（bbolt.Open 在 missing 时会创建空文件不报错）
	if err := s.preCheckMissing(path); err != nil {
		if errors.Is(err, ErrFileMissing) {
			s.notify(ErrorEvent{Op: "open", Path: path, Err: err, Action: "return_error"})
		}
		return err
	}
	db, openErr := bolt.Open(path, 0600, &bolt.Options{
		Timeout: 10 * time.Second,
		NoSync:  !s.cfg.SyncWrites,
	})
	if openErr != nil {
		return s.classifyOpenError(path, openErr)
	}
	defer db.Close()
	return fn(db)
}

// classifyOpenError 把 bolt.Open 错误分类为 ErrFileMissing / ErrFileCorrupt
// 注意：bbolt 在文件不存在时会自动创建新文件（不报错），所以"文件不存在"必须用
// os.Stat 预检查，不能只看 bolt.Open 的错误
func (s *BoltStore) classifyOpenError(path string, openErr error) error {
	// 通用错误信息匹配（兼容 Windows/Linux/macOS）
	if errors.Is(openErr, fs.ErrNotExist) || os.IsNotExist(openErr) {
		s.notify(ErrorEvent{Op: "open", Path: path, Err: openErr, Action: "return_error"})
		return ErrFileMissing
	}
	msg := openErr.Error()
	if contains(msg, "cannot find the path") || contains(msg, "cannot find the file") || contains(msg, "no such file") {
		s.notify(ErrorEvent{Op: "open", Path: path, Err: openErr, Action: "return_error"})
		return ErrFileMissing
	}
	// bbolt 对损坏文件返回 "invalid database" / "checksum error" 等
	log.Printf("ERROR: bbolt file corrupt: %s, err: %v", path, openErr)
	s.notify(ErrorEvent{Op: "open", Path: path, Err: openErr, Action: "return_error"})
	return fmt.Errorf("%w: %v", ErrFileCorrupt, openErr)
}

// preCheckMissing 在 open 之前检查文件是否存在
// 因为 bbolt.Open 在文件不存在时不会报错（会自动创建），所以必须预检查
func (s *BoltStore) preCheckMissing(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ErrFileMissing
		}
		return err
	}
	return nil
}

// readValue 通过 View 事务读取 value
func (s *BoltStore) readValue(db *bolt.DB, key []byte) ([]byte, error) {
	var raw []byte
	err := db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(dataBucket))
		if bk == nil {
			return ErrNotFound
		}
		v := bk.Get(key)
		if v == nil {
			return ErrNotFound
		}
		raw = append([]byte(nil), v...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// getCurrentDB 写路径：复用 current 句柄
func (s *BoltStore) getCurrentDB(ts time.Time) (*bolt.DB, error) {
	path := s.filePath(ts)

	s.mu.Lock()
	if s.currentName == path && s.currentDB != nil {
		db := s.currentDB
		s.mu.Unlock()
		return db, nil
	}
	s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create subdir: %w", err)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{
		Timeout: 10 * time.Second,
		NoSync:  !s.cfg.SyncWrites,
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt open %s: %w", path, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentName == path && s.currentDB != nil {
		_ = db.Close()
		return s.currentDB, nil
	}
	if s.currentDB != nil {
		_ = s.currentDB.Close()
	}
	s.currentDB = db
	s.currentName = path
	return db, nil
}

// compressValue/decompressValue
func (s *BoltStore) compressValue(value []byte) ([]byte, error) {
	if s.cmpr == nil {
		return value, nil
	}
	return s.cmpr.Compress(value)
}
func (s *BoltStore) decompressValue(raw []byte) ([]byte, error) {
	if s.cmpr == nil {
		return raw, nil
	}
	return s.cmpr.Decompress(raw)
}

// invalidateCurrent 关闭并清空 current 句柄（自愈前调用）
func (s *BoltStore) invalidateCurrent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentDB != nil {
		_ = s.currentDB.Close()
		s.currentDB = nil
		s.currentName = ""
	}
}

// InvalidateCurrentForTest 暴露给外部的测试/调试入口
// 关闭 current 句柄释放文件锁（在 Windows 上需要这样才能删除/修改文件）
func (s *BoltStore) InvalidateCurrentForTest() {
	s.invalidateCurrent()
}

// healFile 自愈：删坏文件，让下次 open 重新创建
func (s *BoltStore) healFile(ts time.Time) {
	path := s.filePath(ts)
	s.invalidateCurrent()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("WARN: failed to remove corrupt file %s: %v", path, err)
	}
}

// Put 写入：带自愈的写。文件损坏/丢失时自动删旧建新，**不返回错误**。
func (s *BoltStore) Put(ts time.Time, key []byte, value []byte) error {
	encodedKey := s.encodeKey(ts, key)
	stored, err := s.compressValue(value)
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}

	// 第一次尝试
	err = s.tryPut(ts, encodedKey, stored)
	if err == nil {
		return nil
	}

	// 判断是否可自愈（文件损坏类错误）
	if !isFileLevelError(err) {
		// 不可恢复错误（如编码、压缩），直接返回
		return err
	}

	path := s.filePath(ts)
	log.Printf("WARN: Put file-level error, self-healing: %s, err: %v", path, err)
	s.notify(ErrorEvent{Op: "put", Path: path, Err: err, Action: "self_heal_started"})

	// 自愈：删旧文件
	s.healFile(ts)

	// 重试
	err = s.tryPut(ts, encodedKey, stored)
	if err != nil {
		log.Printf("ERROR: Put still failed after self-heal: %v", err)
		s.notify(ErrorEvent{Op: "put", Path: path, Err: err, Action: "self_heal_failed"})
		return fmt.Errorf("put failed after self-heal: %w", err)
	}

	log.Printf("INFO: Put recovered after self-heal: %s", path)
	s.notify(ErrorEvent{Op: "put", Path: path, Action: "self_heal_succeeded"})
	return nil
}

// tryPut 单次写尝试（不触发自愈）
func (s *BoltStore) tryPut(ts time.Time, encodedKey, stored []byte) error {
	db, err := s.getCurrentDB(ts)
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte(dataBucket))
		if err != nil {
			return fmt.Errorf("create bucket: %w", err)
		}
		return bk.Put(encodedKey, stored)
	})
}

// isFileLevelError 判断是否是文件级错误（可自愈）
func isFileLevelError(err error) bool {
	if err == nil {
		return false
	}
	// ErrFileMissing/ErrFileCorrupt/文件 open 失败 → 可自愈
	if errors.Is(err, ErrFileMissing) || errors.Is(err, ErrFileCorrupt) {
		return true
	}
	// 通用 bbolt open 错误也认为可自愈（删旧重建）
	msg := err.Error()
	return contains(msg, "bbolt open") || contains(msg, "invalid database") || contains(msg, "checksum error")
}

// contains 简单字符串包含（避免引入 strings 包）
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Get 读取：返回 ErrFileMissing / ErrFileCorrupt / ErrNotFound / 数据
func (s *BoltStore) Get(ts time.Time, key []byte) ([]byte, error) {
	encodedKey := s.encodeKey(ts, key)
	path := s.filePath(ts)

	// 命中 current 时复用（free path）
	s.mu.Lock()
	if s.currentName == path && s.currentDB != nil {
		db := s.currentDB
		s.mu.Unlock()
		raw, err := s.readValue(db, encodedKey)
		if err != nil {
			// 命中 current 但读失败：value 损坏或 bucket 异常
			if errors.Is(err, ErrNotFound) {
				return nil, ErrNotFound
			}
			log.Printf("ERROR: read from current failed: %s, err: %v", path, err)
			return nil, fmt.Errorf("%w: %v", ErrFileCorrupt, err)
		}
		return s.decompressValue(raw)
	}
	s.mu.Unlock()

	// 不命中 current：open-close
	var raw []byte
	openErr := s.openAndRead(ts, func(db *bolt.DB) error {
		r, err := s.readValue(db, encodedKey)
		if err != nil {
			return err
		}
		raw = r
		return nil
	})
	if openErr != nil {
		return nil, openErr
	}
	return s.decompressValue(raw)
}

// ListBucket 列出某文件所有 (key, value) 对
func (s *BoltStore) ListBucket(ts time.Time) (map[string][]byte, error) {
	path := s.filePath(ts)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create subdir: %w", err)
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{
		Timeout: 10 * time.Second,
		NoSync:  !s.cfg.SyncWrites,
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string][]byte{}, nil // 空集
		}
		return nil, fmt.Errorf("%w: %v", ErrFileCorrupt, err)
	}
	defer db.Close()
	out := make(map[string][]byte)
	err = db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(dataBucket))
		if bk == nil {
			return nil
		}
		return bk.ForEach(func(k, v []byte) error {
			val, err := s.decompressValue(v)
			if err != nil {
				return err
			}
			out[string(k)] = val
			return nil
		})
	})
	return out, err
}

// FileSize/FileCount：递归遍历 baseDir
func (s *BoltStore) FileSize() int64 {
	var total int64
	filepath.Walk(s.baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		matched, _ := filepath.Match(s.prefix+"-*.db", info.Name())
		if matched {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (s *BoltStore) FileCount() int {
	count := 0
	filepath.Walk(s.baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		matched, _ := filepath.Match(s.prefix+"-*.db", info.Name())
		if matched {
			count++
		}
		return nil
	})
	return count
}

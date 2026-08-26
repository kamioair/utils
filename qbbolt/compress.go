package bboltstore

import (
	"bytes"     // 字节缓冲（避免每次 Compress 分配）
	"compress/gzip" // 标准库 gzip
	"fmt"       // 错误格式化
	"io"        // gzip 解压时读全部
	"sync"      // gzip 单线程安全保护

	"github.com/klauspost/compress/zstd" // 纯 Go 的 zstd 实现
)

// Compressor 抽象接口：写时压缩、读时解压
// 两种实现：zstdCompressor、gzipCompressor
type Compressor interface {
	Compress(src []byte) ([]byte, error)
	Decompress(src []byte) ([]byte, error)
	Close() error
}

// NewCompressor 工厂函数：按 algo 选择 zstd 或 gzip 实现
func NewCompressor(algo string, level int) (Compressor, error) {
	switch algo {
	case "zstd":
		return newZstdCompressor(level)
	case "gzip":
		return newGzipCompressor(level)
	default:
		return nil, fmt.Errorf("unknown compress algo: %s", algo)
	}
}

// ============================================================
// zstd 实现（klauspost/compress，纯 Go，无 cgo 依赖）
// ============================================================

type zstdCompressor struct {
	encoder *zstd.Encoder // 编码器可复用（Reset 后写新数据）
	decoder *zstd.Decoder // 解码器可并发安全使用
}

func newZstdCompressor(level int) (*zstdCompressor, error) {
	// EncoderLevelFromZstd 把 zstd 的 1-22 级映射到 klauspost 的 Speed/Fast/Default/Best
	// level=1-3 对应 Fast/Default，足够
	enc, err := zstd.NewWriter(
		nil,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)),
		zstd.WithEncoderConcurrency(1), // 单线程 demo，无需多核
	)
	if err != nil {
		return nil, fmt.Errorf("zstd encoder: %w", err)
	}
	dec, err := zstd.NewReader(
		nil,
		zstd.WithDecoderConcurrency(1),
	)
	if err != nil {
		enc.Close()
		return nil, fmt.Errorf("zstd decoder: %w", err)
	}
	return &zstdCompressor{encoder: enc, decoder: dec}, nil
}

// Compress 把 src 压缩到新的字节 slice（每次都通过 Reset 重用 encoder）
// 复用的好处是避免每次 NewWriter 分配 buffer
func (c *zstdCompressor) Compress(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	c.encoder.Reset(&buf)
	if _, err := c.encoder.Write(src); err != nil {
		return nil, err
	}
	if err := c.encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decompress 一次性解码（DecodeAll 比 Read+loop 高效）
func (c *zstdCompressor) Decompress(src []byte) ([]byte, error) {
	return c.decoder.DecodeAll(src, nil)
}

func (c *zstdCompressor) Close() error {
	c.encoder.Close()
	c.decoder.Close()
	return nil
}

// ============================================================
// gzip 实现（标准库）
// ============================================================

type gzipCompressor struct {
	level int
	mu    sync.Mutex // gzip.Writer/Reader 不可并发安全共享，加锁保护
}

func newGzipCompressor(level int) (*gzipCompressor, error) {
	return &gzipCompressor{level: level}, nil
}

// Compress 用 gzip 压缩 src
func (c *gzipCompressor) Compress(src []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, c.level)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(src); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decompress 用 gzip 解压 src
func (c *gzipCompressor) Decompress(src []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, err := gzip.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (c *gzipCompressor) Close() error {
	return nil
}
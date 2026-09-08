package qcache

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/patrickmn/go-cache"
)

// defaultWaitTimeout 单飞（singleflight）等待 callback 的默认超时时间。
// 若 findingCallback 在此时间内未完成，等待方将放弃并返回 (zero, false)。
// 显式设为 0 表示永不超时（等待方会一直阻塞直到 callback 完成）。
const defaultWaitTimeout = 30 * time.Second

// Caches 是对 patrickmn/go-cache 的泛型安全包装。
//
// 主要设计目标：
//   - 通过 Go 泛型提供类型安全的缓存接口，避免运行时类型断言失败
//   - 提供 findingCallback 机制，使 Get 在缓存未命中时自动加载
//   - 提供 singleflight（单飞）机制，确保并发请求同一 key 时 callback 仅执行一次
type Caches[T any] struct {
	caches          *cache.Cache
	findingCallback func(key string) (T, bool)
	mu              sync.Mutex // 保护 inFlight 与 waitTimeout 的并发访问
	inFlight        map[string]*inFlightCall[T]
	waitTimeout     time.Duration
	rebuildMu       sync.Mutex // 串行化 Rebuild / SaveToFile / LoadFromFile 等全量操作
	version         int64      // atomic：Rebuild / LoadFromFile 时递增，用于让 in-flight callback 判断是否回写
}

// inFlightCall 记录一次正在执行的 callback 调用，
// 用于实现 singleflight：同一 key 的并发请求共享同一次 callback 执行的结果。
type inFlightCall[T any] struct {
	done    chan struct{} // callback 执行完毕后关闭（无论成功失败）
	value   T
	ok      bool
	version int64 // 注册时缓存版本号；callback 回写前比对，决定是否丢弃
}

// NewCaches 创建缓存。
//
//	@param defaultExpiration 缓存项的默认过期时间。0 表示永不过期，非 0 表示具体过期时长(例如 30 * time.Second)。
//	@param cleanupInterval   清理过期缓存项的时间间隔。0 表示不清理，非 0 表示按该间隔清理(例如 30 * time.Second)。
//	@return *Caches[T]
func NewCaches[T any](defaultExpiration, cleanupInterval time.Duration) *Caches[T] {
	return &Caches[T]{
		caches:      cache.New(defaultExpiration, cleanupInterval),
		inFlight:    make(map[string]*inFlightCall[T]),
		waitTimeout: defaultWaitTimeout,
	}
}

// SetWaitTimeout 设置 singleflight 等待 callback 完成的超时时间。
// 线程安全，可在并发运行期调用。
//
//	@param d 超时时长
//	           - d > 0  : 超过该时长后等待方放弃并返回 (zero, false)
//	           - d == 0 : 永不超时，等待方会一直阻塞直到 callback 完成
//	                      （若 callback 死循环/挂起，可能导致 goroutine 永久堆积，请谨慎使用）
func (c *Caches[T]) SetWaitTimeout(d time.Duration) {
	c.mu.Lock()
	c.waitTimeout = d
	c.mu.Unlock()
}

// SetFindingCallback 设置（或替换、清除）缓存未命中时的主动查找回调。
// 线程安全，可在并发运行期调用。
//
//	@param callback  缓存未命中时的主动查找回调；可为 nil（仅作纯缓存使用）。
//	                 注意：callback 内不应递归访问同一个 Caches 实例的相同 key，
//	                      否则将进入等待-超时分支并返回 (zero, false)（详见 Get 文档）。
func (c *Caches[T]) SetFindingCallback(callback func(key string) (T, bool)) {
	c.mu.Lock()
	c.findingCallback = callback
	c.mu.Unlock()
}

// Set 写入缓存，使用构造时设置的默认过期时间。
// 空 key 会被静默忽略。
//
//	@param key   缓存键
//	@param value 缓存值
func (c *Caches[T]) Set(key string, value T) {
	if key == "" {
		return
	}
	c.caches.Set(key, value, cache.DefaultExpiration)
}

// SetWithNewExpiration 写入缓存，使用自定义过期时间。
// 空 key 会被静默忽略。
//
//	@param key           缓存键
//	@param value         缓存值
//	@param newExpiration 自定义过期时长；为 0 表示永不过期
func (c *Caches[T]) SetWithNewExpiration(key string, value T, newExpiration time.Duration) {
	if key == "" {
		return
	}
	c.caches.Set(key, value, newExpiration)
}

// GetAll 获取所有缓存。
//
// 类型不匹配的条目会被静默跳过，不会触发运行时 panic。
// 已过期但尚未被 cleanup goroutine 清理的条目也会被跳过，保持与 Get 行为一致。
//
//	@return map[string]T 当前缓存的全部未过期条目（至少返回空 map，不会为 nil）
func (c *Caches[T]) GetAll() map[string]T {
	values := map[string]T{}
	now := time.Now().UnixNano()
	for k, v := range c.caches.Items() {
		// 显式过滤过期条目：与 Get 行为保持一致，避免 GetAll 返回 Get 认为"不存在"的值
		if v.Expiration > 0 && now > v.Expiration {
			continue
		}
		// 安全类型断言：避免因 LoadFromFile 加载陈旧文件或类型漂移导致的 panic
		if typed, ok := v.Object.(T); ok {
			values[k] = typed
		}
	}
	return values
}

// Get 获取缓存。
//
// 行为说明：
//  1. 缓存命中且类型匹配：返回值与 true。
//  2. 缓存未命中：若 findingCallback 非 nil，则触发 singleflight 加载；
//     同一 key 的并发请求中仅有一个 goroutine 执行 callback，其余等待结果（最多等待 waitTimeout）。
//     当 waitTimeout == 0 时等待永不超时。
//  3. 超时或 callback 返回 ok=false：返回 (T 的零值, false)。
//  4. 若 callback 执行期间发生了 Rebuild / LoadFromFile，callback 即使返回成功也不会回写到缓存
//     （避免污染全量替换后的新缓存），调用方会拿到 (T 的零值, false)。
//
// 重要：当 T 为指针或接口类型时，请始终用返回的 bool 判定存在性，
// 不要用 nil 判定，因为 nil 本身可能是合法的缓存值。
//
// 重要：findingCallback 内不应调用同一个 Caches 实例的 Get(同 key) 或自身等待的 key。
// 内层 Get 会看到自己设置的 in-flight 并阻塞等待，直至超时返回 (zero, false)，而外层 callback
// 即将返回的真实值会被内层错失。
//
//	@param key 缓存键（空键直接返回零值）
//	@return T  缓存值；未命中时返回 T 的零值
//	@return bool 是否命中
func (c *Caches[T]) Get(key string) (T, bool) {
	var zero T
	if key == "" {
		return zero, false
	}

	// 快速路径：直接命中
	if value, exist := c.caches.Get(key); exist {
		if typed, ok := value.(T); ok {
			return typed, true
		}
		// 类型不匹配视为未命中（不会 panic）
		return zero, false
	}

	c.mu.Lock()
	waitTimeout := c.waitTimeout
	callback := c.findingCallback // 在锁内读取，避免运行期替换造成的 nil 调用
	if pending, exists := c.inFlight[key]; exists {
		c.mu.Unlock()
		// 等待该 key 的 callback 完成
		// waitTimeout == 0 → 永不超时（用 nil channel 实现）
		var timeoutCh <-chan time.Time
		if waitTimeout > 0 {
			timeoutCh = time.After(waitTimeout)
		}
		select {
		case <-pending.done:
			if pending.ok {
				return pending.value, true
			}
			return zero, false
		case <-timeoutCh:
			return zero, false
		}
	}

	// 取锁与加锁之间 callback 可能已完成，再次检查缓存（避免 TOCTOU 导致重复执行）
	if v, ok := c.caches.Get(key); ok {
		c.mu.Unlock()
		if typed, ok := v.(T); ok {
			return typed, true
		}
		return zero, false
	}

	// 锁内再次确认 callback 非 nil（覆盖运行期被置空的边界）
	if callback == nil {
		c.mu.Unlock()
		return zero, false
	}

	// 注册为 in-flight，使用 defer 确保 callback panic 时也能清理与唤醒等待方
	pending := &inFlightCall[T]{
		done:    make(chan struct{}),
		version: atomic.LoadInt64(&c.version), // 捕获注册时刻的版本号
	}
	c.inFlight[key] = pending
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.inFlight, key)
		c.mu.Unlock()
		// 关闭 done 必须在删除 inFlight 之后，避免等待方被唤醒后又看到 pending 被其他路径访问
		close(pending.done)
	}()

	// 使用锁内读取的 callback 引用执行，即使运行期被替换也不影响本次调用
	pending.value, pending.ok = callback(key)

	if pending.ok {
		// 版本号校验：若期间发生了 Rebuild / LoadFromFile，回写会污染新缓存。
		// 此时仅通知等待方"callback 失败"，不写入缓存，调用方应自行重试或 fallback。
		if atomic.LoadInt64(&c.version) == pending.version {
			// 使用 Add 而非 Set：仅当 key 不存在时写入，避免覆盖其他 goroutine 的并发 Set
			c.caches.Add(key, pending.value, cache.DefaultExpiration)
			return pending.value, true
		}
		return zero, false
	}
	return zero, false
}

// Delete 删除缓存。空 key 会被静默忽略。
//
//	@param key 缓存键
func (c *Caches[T]) Delete(key string) {
	if key == "" {
		return
	}
	c.caches.Delete(key)
}

// ClearAll 清空所有缓存项。
// 不会影响 inFlight 中的 callback；正在执行的 callback 完成后仍会将其结果回写到缓存中。
// 该操作会短暂锁定底层缓存，期间 Get/Set 会阻塞。
func (c *Caches[T]) ClearAll() {
	c.caches.Flush()
}

// Rebuild 清空当前缓存并使用 data 重建（尽力权威语义）。
//
// 实现步骤：
//  1. 先递增版本号（让正在执行的 in-flight callback 后续回写时被识别为失效）。
//  2. 调用 ClearAll 一次性清空所有现有条目。
//  3. 逐条写入 data 中的 key/value，写入使用构造时设置的默认过期时间。
//
// 并发语义：
//  - 同一时刻仅允许一个 Rebuild 执行：并发调用将被串行化，等待前一次完成。
//    防止两个 Rebuild 交错产生既不属于 A 也不属于 B 的混合状态。
//  - Rebuild 是"尽力权威"：在 Flush 与 Set 窗口内发生的并发 Set 会被 Rebuild 覆盖；
//    但若 Rebuild 完成 Set 后才有并发 Set，则并发 Set 会反过来覆盖 Rebuild 的值
//    （无跨所有写入的全局互斥，最后写入者胜出）。
//  - 清空与写入不是原子操作，期间并发 Get 可能短暂看到空缓存或部分新数据。
//  - 正在执行的 findingCallback 仍会执行完成，但其结果**不会回写到新缓存**（由版本号机制保证）。
//    调用方此时会拿到 (zero, false)，应自行重试或 fallback。
//
// 其他约束：
//  - data 中空字符串 key 会被静默忽略（与 Set 行为一致）。
//  - data 为 nil 或空 map 时，仅执行清空，等价于 ClearAll。
//
//	@param data 用于重建缓存的键值对集合
func (c *Caches[T]) Rebuild(data map[string]T) {
	c.rebuildMu.Lock()
	defer c.rebuildMu.Unlock()

	// 递增版本号：让 in-flight callback 后续 Add 时被识别为失效版本
	atomic.AddInt64(&c.version, 1)

	c.caches.Flush()
	for k, v := range data {
		if k == "" {
			continue
		}
		c.caches.Set(k, v, cache.DefaultExpiration)
	}
}

// SaveToFile 将缓存序列化到文件。
// 该操作与 Rebuild / LoadFromFile 互斥：调用期间会等待所有进行中的 Rebuild 完成，
// 避免落盘文件包含 Rebuild 的部分中间状态。
//
//	@param filePath 文件路径
//	@return error 序列化过程中的错误
func (c *Caches[T]) SaveToFile(filePath string) error {
	c.rebuildMu.Lock()
	defer c.rebuildMu.Unlock()
	return c.caches.SaveFile(filePath)
}

// LoadFromFile 从文件加载缓存（全量替换语义）。
//
// 行为：先清空当前所有缓存项，然后从文件加载新内容。
// 这与 go-cache 底层 LoadFile 的"合并到现有条目"语义不同——本方法显式 Flush 以保证替换语义，
// 避免与 Rebuild 并发时产生 Frankenstein 状态。
//
// 警告 1：若文件加载失败（文件不存在、格式错误等），当前所有缓存项已被清空。
// 调用方若需保证数据完整性，应在加载前先 SaveToFile 备份。
//
// 警告 2：调用前会递增缓存版本号，正在执行的 findingCallback 不会将其结果回写到加载后的缓存中。
// 该操作与 Rebuild 互斥：调用期间会等待所有进行中的 Rebuild 完成。
//
//	@param filePath 文件路径
//	@return error 加载过程中的错误（即使发生错误，缓存已被清空）
func (c *Caches[T]) LoadFromFile(filePath string) error {
	c.rebuildMu.Lock()
	defer c.rebuildMu.Unlock()

	// 递增版本号：让 in-flight callback 后续 Add 时被识别为失效版本，避免污染加载后的新缓存
	atomic.AddInt64(&c.version, 1)
	// 显式 Flush：go-cache 底层 LoadFile 是"合并到现有条目"语义，
	// 这里先清空再加载，保证替换语义以避免 Frankenstein
	c.caches.Flush()
	return c.caches.LoadFile(filePath)
}

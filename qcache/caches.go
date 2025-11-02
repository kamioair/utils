package qcache

import (
	"github.com/patrickmn/go-cache"
	"sync"
	"time"
)

type Caches[T any] struct {
	caches          *cache.Cache
	findingCallback func(key string) (T, bool)
	mu              sync.RWMutex        // 用于保护findingCallback的并发执行
	callbackKeys    map[string]struct{} // 记录正在执行callback的key，防止重复执行
}

// NewCaches 创建缓存
//
//	@param defaultExpiration 缓存项的默认过期时间
//	@param cleanupInterval 清理过期缓存项的时间间隔 0不清理 非0间隔清理
//	@param findingCallback Get缓存不存在时，主动查找回调方法
//	@return *Caches[T]
func NewCaches[T any](defaultExpiration, cleanupInterval time.Duration, findingCallback func(key string) (T, bool)) *Caches[T] {
	c := &Caches[T]{
		caches:          cache.New(defaultExpiration, cleanupInterval),
		findingCallback: findingCallback,
		callbackKeys:    make(map[string]struct{}),
	}
	return c
}

// Set 写入缓存，使用默认的缓存有效期
//
//	@param key
//	@param value
//	@param newExpiration
func (c *Caches[T]) Set(key string, value T) {
	if key == "" {
		return // 忽略空的key
	}
	c.caches.Set(key, value, cache.DefaultExpiration)
}

// SetWithNewExpiration 写入缓存, 使用新的缓存有效期
//
//	@param key
//	@param value
//	@param newExpiration
func (c *Caches[T]) SetWithNewExpiration(key string, value T, newExpiration time.Duration) {
	if key == "" {
		return // 忽略空的key
	}
	c.caches.Set(key, value, newExpiration)
}

// Get 获取缓存
//
//	@param key
//	@return T
func (c *Caches[T]) Get(key string) (T, bool) {
	// 检查key是否为空
	if key == "" {
		var zero T
		return zero, false
	}

	value, exist := c.caches.Get(key)
	if exist == false && c.findingCallback != nil {
		// 检查是否有其他goroutine正在为这个key执行callback
		c.mu.Lock()
		if _, inProgress := c.callbackKeys[key]; inProgress {
			// 如果有其他goroutine正在执行callback，等待并重新尝试获取
			c.mu.Unlock()
			// 简单的重试策略，最多重试3次
			for i := 0; i < 3; i++ {
				value, exist = c.caches.Get(key)
				if exist {
					break
				}
				time.Sleep(time.Millisecond * 10) // 等待10ms
			}
		} else {
			// 标记这个key正在执行callback
			c.callbackKeys[key] = struct{}{}
			c.mu.Unlock()

			// 执行callback
			newValue, ok := c.findingCallback(key)

			// 清除标记
			c.mu.Lock()
			delete(c.callbackKeys, key)
			c.mu.Unlock()

			if ok == true {
				c.caches.Set(key, newValue, cache.DefaultExpiration)
				value = newValue
				exist = true
			}
		}
	}

	if exist == false {
		var zero T
		return zero, false
	}

	// 安全的类型断言
	if typedValue, ok := value.(T); ok {
		return typedValue, exist
	}

	// 类型断言失败，返回零值
	var zero T
	return zero, false
}

// Delete 删除缓存
//
//	@param key
func (c *Caches[T]) Delete(key string) {
	if key == "" {
		return // 忽略空的key
	}
	c.caches.Delete(key)
}

// SaveToFile 将缓存保存到文件
//
//	@param filePath 文件路径
//	@return error
func (c *Caches[T]) SaveToFile(filePath string) error {
	return c.caches.SaveFile(filePath)
}

// LoadFromFile 从文件加载缓存
//
//	@param filePath 文件路径
//	@return error
func (c *Caches[T]) LoadFromFile(filePath string) error {
	return c.caches.LoadFile(filePath)
}

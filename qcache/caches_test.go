package qcache

import (
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"
)

// TestGetAll_TypeMismatch_NoPanic 验证 GetAll 遇到类型不匹配的数据时不会 panic。
func TestGetAll_TypeMismatch_NoPanic(t *testing.T) {
	c := NewCaches[string](time.Minute, time.Minute)
	c.Set("good", "value")
	// 手动塞入一个类型不匹配的条目（绕过泛型 API 直接写入底层）
	c.caches.Set("bad", 123, cache.DefaultExpiration)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("GetAll should not panic on type mismatch, got: %v", r)
		}
	}()

	all := c.GetAll()
	if _, ok := all["good"]; !ok {
		t.Errorf("expected 'good' key to be present")
	}
	if _, ok := all["bad"]; ok {
		t.Errorf("type-mismatched 'bad' key should be skipped, but was included")
	}
}

// TestGet_Singleflight_OnlyOneCallback 验证并发请求同一 key 时 callback 仅执行一次。
func TestGet_Singleflight_OnlyOneCallback(t *testing.T) {
	var callbackCount int32
	var callbackStarted = make(chan struct{}, 1)
	var callbackRelease = make(chan struct{})

	c := NewCaches[string](time.Minute, time.Minute)
	c.SetFindingCallback(func(key string) (string, bool) {
		atomic.AddInt32(&callbackCount, 1)
		// 通知第一个等待者 callback 已开始
		select {
		case callbackStarted <- struct{}{}:
		default:
		}
		// 模拟慢加载
		<-callbackRelease
		return "loaded-" + key, true
	})

	const goroutines = 20
	var wg sync.WaitGroup
	results := make([]string, goroutines)
	existsFlags := make([]bool, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			v, ok := c.Get("k1")
			results[idx] = v
			existsFlags[idx] = ok
		}(i)
	}

	// 等到 callback 开始执行
	<-callbackStarted
	// 让 callback 释放
	close(callbackRelease)
	wg.Wait()

	if got := atomic.LoadInt32(&callbackCount); got != 1 {
		t.Errorf("expected callback to run exactly once, got %d", got)
	}
	for i, v := range results {
		if !existsFlags[i] {
			t.Errorf("goroutine %d: expected ok=true, got false", i)
		}
		if v != "loaded-k1" {
			t.Errorf("goroutine %d: expected 'loaded-k1', got %q", i, v)
		}
	}
}

// TestGet_CallbackPanic_DoesNotLeakState 验证 callback panic 后，inFlight map 被清理。
// 后续请求应能重新触发 callback，而不是永远等待。
func TestGet_CallbackPanic_DoesNotLeakState(t *testing.T) {
	var callbackCount int32
	c := NewCaches[string](time.Minute, time.Minute)
	c.SetFindingCallback(func(key string) (string, bool) {
		n := atomic.AddInt32(&callbackCount, 1)
		if n == 1 {
			panic("simulated callback failure")
		}
		return "ok-" + key, true
	})

	// 用子 goroutine + recover 包装调用，避免自身 panic 拖垮测试
	done := make(chan struct{})
	go func() {
		defer func() {
			_ = recover()
			close(done)
		}()
		c.Get("panic-key")
	}()
	<-done

	// 验证 inFlight 已被清理：第二次 Get 应能正常获得结果
	v, ok := c.Get("panic-key")
	if !ok {
		t.Errorf("expected second Get to succeed after panic recovery, got ok=false")
	}
	if v != "ok-panic-key" {
		t.Errorf("expected 'ok-panic-key', got %q", v)
	}

	// 确认第二次 Get 确实又触发了 callback（不是 panic 路径返回的）
	if got := atomic.LoadInt32(&callbackCount); got != 2 {
		t.Errorf("expected callback to run twice (once panicked, once recovered), got %d", got)
	}
}

// TestSetFindingCallback_HotSwap 验证运行期替换 callback 后，新请求立即使用新实现。
func TestSetFindingCallback_HotSwap(t *testing.T) {
	var oldCount, newCount int32
	c := NewCaches[string](time.Minute, time.Minute)
	c.SetFindingCallback(func(key string) (string, bool) {
		atomic.AddInt32(&oldCount, 1)
		return "old-" + key, true
	})

	// 触发旧 callback
	if v, _ := c.Get("k"); v != "old-k" {
		t.Fatalf("expected 'old-k' from initial callback, got %q", v)
	}
	if atomic.LoadInt32(&oldCount) != 1 {
		t.Fatalf("old callback should have run once")
	}

	// 运行期替换 callback
	c.SetFindingCallback(func(key string) (string, bool) {
		atomic.AddInt32(&newCount, 1)
		return "new-" + key, true
	})

	// 删除已有缓存以触发新 callback
	c.Delete("k")

	if v, _ := c.Get("k"); v != "new-k" {
		t.Errorf("expected 'new-k' after hot-swap, got %q", v)
	}
	if atomic.LoadInt32(&newCount) != 1 {
		t.Errorf("new callback should have run once, got %d", newCount)
	}
	if atomic.LoadInt32(&oldCount) != 1 {
		t.Errorf("old callback should not have run again, got total=%d", oldCount)
	}
}

// TestSetFindingCallback_DisableWithNil 验证通过 SetFindingCallback(nil) 可以禁用自动加载。
func TestSetFindingCallback_DisableWithNil(t *testing.T) {
	var count int32
	c := NewCaches[string](time.Minute, time.Minute)
	c.SetFindingCallback(func(key string) (string, bool) {
		atomic.AddInt32(&count, 1)
		return "v", true
	})

	c.SetFindingCallback(nil)

	v, ok := c.Get("k")
	if ok {
		t.Errorf("expected ok=false after disabling callback, got true with v=%q", v)
	}
	if atomic.LoadInt32(&count) != 0 {
		t.Errorf("disabled callback should not run, got count=%d", count)
	}
}

// TestGet_CallbackWriteBack_DoesNotOverwriteConcurrentSet
// 验证 callback 回写使用 Add，不会覆盖其他 goroutine 的并发 Set。
func TestGet_CallbackWriteBack_DoesNotOverwriteConcurrentSet(t *testing.T) {
	c := NewCaches[string](time.Minute, time.Minute)
	c.SetFindingCallback(func(key string) (string, bool) {
		time.Sleep(50 * time.Millisecond) // 给并发 Set 留时间窗口
		return "from-callback", true
	})

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1：触发 callback
	go func() {
		defer wg.Done()
		c.Get("k")
	}()

	// Goroutine 2：在 callback 执行期间 Set 一个值
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
		c.Set("k", "from-set")
	}()

	wg.Wait()

	// 最终值应为"from-set"，callback 回写不应覆盖
	if v, ok := c.Get("k"); !ok || v != "from-set" {
		t.Errorf("expected 'from-set' (concurrent Set should win), got %q, ok=%v", v, ok)
	}
}

// TestClearAll 验证 ClearAll 一次性清空所有条目。
func TestClearAll(t *testing.T) {
	c := NewCaches[string](time.Minute, time.Minute)
	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3")

	if len(c.GetAll()) != 3 {
		t.Fatalf("setup: expected 3 entries, got %d", len(c.GetAll()))
	}

	c.ClearAll()

	if got := len(c.GetAll()); got != 0 {
		t.Errorf("expected 0 entries after ClearAll, got %d", got)
	}
}

// TestRebuild_FromMap 验证 Rebuild 清空旧条目后写入新条目。
func TestRebuild_FromMap(t *testing.T) {
	c := NewCaches[string](time.Minute, time.Minute)
	c.Set("old1", "x")
	c.Set("old2", "y")

	c.Rebuild(map[string]string{
		"new1": "1",
		"new2": "2",
	})

	all := c.GetAll()
	if len(all) != 2 {
		t.Errorf("expected 2 entries after Rebuild, got %d (%v)", len(all), all)
	}
	if v, _ := c.Get("new1"); v != "1" {
		t.Errorf("expected new1=1, got %q", v)
	}
	if v, _ := c.Get("new2"); v != "2" {
		t.Errorf("expected new2=2, got %q", v)
	}
	if _, ok := c.Get("old1"); ok {
		t.Errorf("old1 should have been cleared by Rebuild")
	}
	if _, ok := c.Get("old2"); ok {
		t.Errorf("old2 should have been cleared by Rebuild")
	}
}

// TestRebuild_NilOrEmptyMap 验证传入 nil 或空 map 时，Rebuild 等价于 ClearAll。
func TestRebuild_NilOrEmptyMap(t *testing.T) {
	cases := []struct {
		name string
		data map[string]string
	}{
		{"nil", nil},
		{"empty", map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCaches[string](time.Minute, time.Minute)
			c.Set("k", "v")
			c.Rebuild(tc.data)
			if len(c.GetAll()) != 0 {
				t.Errorf("expected cache to be empty, got %v", c.GetAll())
			}
		})
	}
}

// TestRebuild_SkipsEmptyKeys 验证 Rebuild 对空字符串 key 的处理与 Set 一致。
func TestRebuild_SkipsEmptyKeys(t *testing.T) {
	c := NewCaches[string](time.Minute, time.Minute)
	c.Rebuild(map[string]string{
		"":   "ignored",
		"k1": "v1",
	})
	if _, ok := c.Get(""); ok {
		t.Errorf("empty key should not be stored")
	}
	if v, ok := c.Get("k1"); !ok || v != "v1" {
		t.Errorf("expected k1=v1, got %q, ok=%v", v, ok)
	}
}

// TestRebuild_ConcurrentRebuildsSerialized 验证并发 Rebuild 不会产生 Frankenstein 状态。
// 通过 rebuildMu 串行化，最终所有 key 的 value 必须全部等于 "A" 或全部等于 "B"。
func TestRebuild_ConcurrentRebuildsSerialized(t *testing.T) {
	c := NewCaches[string](time.Minute, time.Minute)

	const n = 200
	dataA := make(map[string]string, n)
	dataB := make(map[string]string, n)
	for i := 0; i < n; i++ {
		k := "k" + strconv.Itoa(i)
		dataA[k] = "A"
		dataB[k] = "B"
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.Rebuild(dataA) }()
	go func() { defer wg.Done(); c.Rebuild(dataB) }()
	wg.Wait()

	all := c.GetAll()
	if len(all) != n {
		t.Fatalf("expected %d entries, got %d", n, len(all))
	}

	var firstVal string
	for k, v := range all {
		if firstVal == "" {
			firstVal = v
			continue
		}
		if v != firstVal {
			t.Fatalf("Frankenstein state detected at key=%s: first=%q, current=%q",
				k, firstVal, v)
		}
	}
	if firstVal != "A" && firstVal != "B" {
		t.Fatalf("expected all values 'A' or all 'B', got %q", firstVal)
	}
}

// TestRebuild_RaceWindow 验证 Rebuild 的权威语义是"尽力而为"：
// 在 Flush 与 Set 窗口内发生的 Set 会被 Rebuild 覆盖；窗口外的 Set 行为未定义。
// 该测试不验证具体胜负结果，只验证调用序列不会死锁或 panic。
func TestRebuild_RaceWindow(t *testing.T) {
	c := NewCaches[string](time.Minute, time.Minute)

	data := make(map[string]string, 200)
	for i := 0; i < 200; i++ {
		data["k"+strconv.Itoa(i)] = "REBUILT"
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 在并发 Set 的同时调用 Rebuild
		for i := 0; i < 1000; i++ {
			c.Set("k"+strconv.Itoa(i%200), "USER")
			c.Rebuild(data)
		}
	}()

	<-done

	// 不验证具体值（属于 race window），只验证 Get/GetAll 不 panic
	all := c.GetAll()
	if len(all) == 0 {
		t.Errorf("cache should not be empty after concurrent Rebuild+Set")
	}
}

// TestSetWaitTimeout_ZeroMeansNoTimeout 验证 S1 修复：
// waitTimeout=0 表示永不超时，等待方阻塞至 callback 完成，而非立即超时。
func TestSetWaitTimeout_ZeroMeansNoTimeout(t *testing.T) {
	callbackStarted := make(chan struct{})
	callbackRelease := make(chan struct{})
	c := NewCaches[string](time.Minute, time.Minute)
	c.SetWaitTimeout(0) // 关键：永不超时
	c.SetFindingCallback(func(key string) (string, bool) {
		close(callbackStarted)
		<-callbackRelease
		return "value", true
	})

	// 第一个调用触发 callback（会阻塞）
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		c.Get("k")
	}()
	<-callbackStarted

	// 第二个调用应等待（按 S1 修复后不会立即超时）
	secondDone := make(chan struct{})
	var secondVal string
	var secondOk bool
	go func() {
		defer close(secondDone)
		secondVal, secondOk = c.Get("k")
	}()

	select {
	case <-secondDone:
		t.Fatalf("second Get returned before callback completed; waitTimeout=0 should not mean immediate timeout")
	case <-time.After(100 * time.Millisecond):
		// 符合预期：仍在等待
	}

	// 释放 callback
	close(callbackRelease)
	<-firstDone

	// 第二个调用现在应能拿到结果
	select {
	case <-secondDone:
		if !secondOk || secondVal != "value" {
			t.Errorf("expected (value, true), got (%q, %v)", secondVal, secondOk)
		}
	case <-time.After(time.Second):
		t.Fatalf("second Get did not return after callback completed")
	}
}

// TestGetAll_FiltersExpired 验证 S3 修复：GetAll 显式过滤过期条目，与 Get 行为一致。
func TestGetAll_FiltersExpired(t *testing.T) {
	// 用短的 cleanupInterval 让 expired 项被及时清理
	// "alive" 用永不过期，"dies" 用 30ms 过期
	c := NewCaches[string](0, 10*time.Millisecond)
	c.SetWithNewExpiration("alive", "v1", 0) // 0 = 永不过期
	c.SetWithNewExpiration("dies", "v2", 30*time.Millisecond)

	// 等到过期 + cleanup
	time.Sleep(80 * time.Millisecond)

	// 直接断言 Get 返回 false（已过期）
	if _, ok := c.Get("dies"); ok {
		t.Fatalf("setup: Get('dies') should return ok=false after expiration")
	}

	all := c.GetAll()
	if _, ok := all["alive"]; !ok {
		t.Errorf("expected 'alive' to be present")
	}
	if _, ok := all["dies"]; ok {
		t.Errorf("expected 'dies' to be filtered out (expired), but it was present")
	}
}

// TestRebuild_DoesNotPolluteFromInFlight 验证 S6 修复：
// Rebuild 期间 in-flight callback 的结果不回写到新缓存，避免污染。
// 通过 GetAll 检查缓存状态而非 Get（避免触发 callback 重新执行）。
func TestRebuild_DoesNotPolluteFromInFlight(t *testing.T) {
	callbackStarted := make(chan struct{})
	callbackRelease := make(chan struct{})
	c := NewCaches[string](time.Minute, time.Minute)
	c.SetFindingCallback(func(key string) (string, bool) {
		close(callbackStarted)
		<-callbackRelease
		return "from-callback", true
	})

	// 触发 callback（cache miss）
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		c.Get("k")
	}()
	<-callbackStarted

	// Rebuild（不包含 "k"），版本号递增
	c.Rebuild(map[string]string{"other": "v"})

	// 释放 callback，让第一个 Get 完成
	close(callbackRelease)
	<-firstDone

	// 用 GetAll 检查缓存状态，避免再次触发 callback
	all := c.GetAll()
	if _, ok := all["k"]; ok {
		t.Errorf("expected 'k' to not be in cache after Rebuild invalidated in-flight callback")
	}
	if _, ok := all["other"]; !ok {
		t.Errorf("expected 'other' from Rebuild to be in cache")
	}
}

// TestSaveToFile_WaitsForRebuild 验证 S4 修复：
// SaveToFile 与 Rebuild 通过 rebuildMu 串行化，落盘文件不会包含 Rebuild 的中间状态。
// 即：要么 SaveToFile 在 Rebuild 之前（文件为空），要么之后（文件含完整数据），不会部分写入。
func TestSaveToFile_WaitsForRebuild(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "cache.dat")
	c := NewCaches[string](time.Minute, time.Minute)

	data := make(map[string]string, 500)
	for i := 0; i < 500; i++ {
		data["k"+strconv.Itoa(i)] = "v"
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.Rebuild(data)
	}()
	go func() {
		defer wg.Done()
		if err := c.SaveToFile(tmpFile); err != nil {
			t.Errorf("SaveToFile error: %v", err)
		}
	}()
	wg.Wait()

	// 验证：要么文件为空（SaveToFile 在 Rebuild 之前），要么 500 条（之后），不允许中间值
	c2 := NewCaches[string](time.Minute, time.Minute)
	if err := c2.LoadFromFile(tmpFile); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	got := len(c2.GetAll())
	if got != 0 && got != 500 {
		t.Errorf("file must contain 0 or 500 entries (serialized states), got %d", got)
	}
}

// TestLoadFromFile_NotOverwrittenByConcurrentRebuild 验证 S5 修复：
// LoadFromFile 与 Rebuild 通过 rebuildMu 串行化，加载结果不会被 Rebuild 覆盖成 Frankenstein。
func TestLoadFromFile_NotOverwrittenByConcurrentRebuild(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "cache.dat")

	// 准备文件
	c1 := NewCaches[string](time.Minute, time.Minute)
	for i := 0; i < 100; i++ {
		c1.Set("fromfile"+strconv.Itoa(i), "X")
	}
	if err := c1.SaveToFile(tmpFile); err != nil {
		t.Fatalf("setup SaveToFile: %v", err)
	}

	// 新的 cache，并发 Load 与 Rebuild
	c2 := NewCaches[string](time.Minute, time.Minute)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c2.Rebuild(map[string]string{"rebuild": "R"})
	}()
	go func() {
		defer wg.Done()
		if err := c2.LoadFromFile(tmpFile); err != nil {
			t.Errorf("LoadFromFile: %v", err)
		}
	}()
	wg.Wait()

	// 最终状态必须是文件内容 或 Rebuild 内容，不应混合
	all := c2.GetAll()
	var hasFile, hasRebuild bool
	for k := range all {
		if k == "rebuild" {
			hasRebuild = true
		}
		if len(k) > 8 && k[:8] == "fromfile" {
			hasFile = true
		}
	}
	if hasFile && hasRebuild {
		t.Errorf("Frankenstein state detected: file and Rebuild data mixed: %v", all)
	}
	if !hasFile && !hasRebuild {
		t.Errorf("expected either file data or Rebuild data, got %v", all)
	}
}

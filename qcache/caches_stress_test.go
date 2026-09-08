package qcache

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStress_NoGoroutineLeak 验证大量并发 Get 不会泄漏 goroutine 与 inFlight 条目。
func TestStress_NoGoroutineLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in short mode")
	}

	baseline := runtime.NumGoroutine()

	c := NewCaches[string](time.Minute, time.Minute)
	var callbackCount int32
	c.SetFindingCallback(func(key string) (string, bool) {
		atomic.AddInt32(&callbackCount, 1)
		// 模拟少量 IO 耗时，便于 singleflight 真正生效
		time.Sleep(time.Microsecond)
		return "v-" + key, true
	})

	const goroutines = 100
	const opsPerG = 1000
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerG; j++ {
				key := fmt.Sprintf("k%d", id%10)
				c.Get(key)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 等待可能的清理
	time.Sleep(50 * time.Millisecond)

	final := runtime.NumGoroutine()
	leaked := final - baseline

	c.mu.Lock()
	inFlightCount := len(c.inFlight)
	c.mu.Unlock()

	t.Logf("ops=%d, callbacks=%d, elapsed=%v, goroutines baseline=%d final=%d leaked=%d, inFlight残留=%d",
		goroutines*opsPerG, callbackCount, elapsed, baseline, final, leaked, inFlightCount)

	if leaked > 5 {
		t.Errorf("goroutine leak detected: %d (baseline=%d final=%d)", leaked, baseline, final)
	}
	if inFlightCount != 0 {
		t.Errorf("inFlight map 未清理: %d 条残留", inFlightCount)
	}
}

// TestStress_SingleflightInvariant 验证同一 key 的并发 Get 不会重复触发 callback。
func TestStress_SingleflightInvariant(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in short mode")
	}

	c := NewCaches[string](time.Millisecond*10, time.Millisecond) // 短过期迫使反复 miss
	var callbackCount int32
	c.SetFindingCallback(func(key string) (string, bool) {
		atomic.AddInt32(&callbackCount, 1)
		time.Sleep(time.Millisecond) // 慢 callback，确保 singleflight 窗口存在
		return "v", true
	})

	// 删除缓存以触发新一轮 callback
	resetCache := func() {
		for i := 0; i < 10; i++ {
			c.Delete(fmt.Sprintf("k%d", i))
		}
	}

	const rounds = 20
	for r := 0; r < rounds; r++ {
		beforeCallback := atomic.LoadInt32(&callbackCount)

		resetCache()

		var wg sync.WaitGroup
		const fanout = 50
		for i := 0; i < fanout; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				c.Get(fmt.Sprintf("k%d", id%10))
			}(i)
		}
		wg.Wait()

		afterCallback := atomic.LoadInt32(&callbackCount)
		executedThisRound := afterCallback - beforeCallback

		// 10 个 key，每个 key 应只触发 1 次 callback（首次 miss 后写回）
		if executedThisRound > 10 {
			t.Errorf("round %d: callback 执行 %d 次，超过 10 个 key 的预期上限，singleflight 失效", r, executedThisRound)
		}
	}

	t.Logf("rounds=%d, 总 callback 次数=%d (理想: ≤ %d)", rounds, callbackCount, rounds*10)
}

// TestStress_RebuildStorm 验证大量并发 Rebuild 不会产生 Frankenstein 状态。
func TestStress_RebuildStorm(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in short mode")
	}

	c := NewCaches[string](time.Minute, time.Minute)

	const n = 100
	datasets := make([]map[string]string, n)
	for i := 0; i < n; i++ {
		d := make(map[string]string, 100)
		for j := 0; j < 100; j++ {
			d[fmt.Sprintf("k%d", j)] = fmt.Sprintf("v%d", i)
		}
		datasets[i] = d
	}

	var wg sync.WaitGroup
	wg.Add(n)
	start := time.Now()
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			c.Rebuild(datasets[idx])
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	all := c.GetAll()
	if len(all) != 100 {
		t.Fatalf("expected 100 entries, got %d", len(all))
	}

	// 验证所有 value 来自同一个 dataset
	firstVal := ""
	for _, v := range all {
		if firstVal == "" {
			firstVal = v
		} else if v != firstVal {
			t.Fatalf("Frankenstein detected: value mixed (%q vs %q)", firstVal, v)
		}
	}
	t.Logf("%d 并发 Rebuild 完成, elapsed=%v, 最终 value=%q (来自某一个 dataset)", n, elapsed, firstVal)
}

// TestStress_PanicStorm 验证 callback 频繁 panic 时 inFlight 仍能正确清理。
func TestStress_PanicStorm(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in short mode")
	}

	c := NewCaches[string](time.Minute, time.Minute)
	var panicCount int32
	c.SetFindingCallback(func(key string) (string, bool) {
		n := atomic.AddInt32(&panicCount, 1)
		if n%3 == 0 {
			panic("simulated callback failure")
		}
		return "v-" + key, true
	})

	var panicsRecovered int32
	var successfulGets int32

	var wg sync.WaitGroup
	const goroutines = 50
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					atomic.AddInt32(&panicsRecovered, 1)
				}
			}()
			for j := 0; j < 100; j++ {
				_, ok := c.Get(fmt.Sprintf("k%d-%d", id, j%5))
				if ok {
					atomic.AddInt32(&successfulGets, 1)
				}
			}
		}(i)
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond)

	c.mu.Lock()
	inFlightCount := len(c.inFlight)
	c.mu.Unlock()

	t.Logf("callback 总调用=%d (其中 panic %d 次), 业务侧 recover=%d 次, 成功 Get=%d, inFlight残留=%d",
		panicCount, panicCount/3, panicsRecovered, successfulGets, inFlightCount)

	if inFlightCount != 0 {
		t.Errorf("inFlight 残留 %d 条（panic 路径未清理）", inFlightCount)
	}
	if successfulGets == 0 {
		t.Errorf("expected some successful Gets, got 0")
	}
}

// TestStress_VersionInvalidation 验证 callback 执行期间发生 Rebuild，回写被丢弃。
func TestStress_VersionInvalidation(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in short mode")
	}

	c := NewCaches[string](time.Minute, time.Minute)
	var callbackCount int32
	var writebacksAttempted int32
	c.SetFindingCallback(func(key string) (string, bool) {
		atomic.AddInt32(&callbackCount, 1)
		time.Sleep(50 * time.Millisecond) // 慢 callback
		atomic.AddInt32(&writebacksAttempted, 1)
		return "from-callback", true
	})

	const polluteKey = "pollute-key"

	// 触发 callback（cache miss）
	go func() {
		defer func() { _ = recover() }()
		c.Get(polluteKey)
	}()

	// 等 callback 开始
	time.Sleep(10 * time.Millisecond)

	// 期间 Rebuild 一个不包含 polluteKey 的数据集
	c.Rebuild(map[string]string{"unrelated": "v"})

	// 等待 callback 完成
	time.Sleep(100 * time.Millisecond)

	// 验证 polluteKey 没有被回写
	if _, ok := c.GetAll()[polluteKey]; ok {
		t.Errorf("Rebuild 后 in-flight callback 仍回写了 key=%s，版本号机制失效", polluteKey)
	}

	t.Logf("callback=%d, 写回尝试=%d", atomic.LoadInt32(&callbackCount), atomic.LoadInt32(&writebacksAttempted))
}

// TestStress_NoDeadlock 验证混合高频操作下不会死锁（带超时）。
func TestStress_NoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in short mode")
	}

	c := NewCaches[string](time.Minute, time.Minute)
	c.SetFindingCallback(func(key string) (string, bool) {
		time.Sleep(time.Microsecond * 100)
		return "v", true
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		const goroutines = 30
		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < 200; j++ {
					switch j % 5 {
					case 0:
						c.Set(fmt.Sprintf("k%d", id), "v")
					case 1:
						c.Get(fmt.Sprintf("k%d", id))
					case 2:
						c.Delete(fmt.Sprintf("k%d", id))
					case 3:
						c.GetAll()
					case 4:
						c.Rebuild(map[string]string{fmt.Sprintf("k%d", id): "r"})
					}
				}
			}(i)
		}
		wg.Wait()
	}()

	select {
	case <-done:
		t.Log("混合操作压力测试完成，无死锁")
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock detected: 测试超时未完成")
	}
}

// BenchmarkGetHit 基准测试：缓存命中的 Get。
func BenchmarkGetHit(b *testing.B) {
	c := NewCaches[int](time.Minute, time.Minute)
	for i := 0; i < 1000; i++ {
		c.Set(fmt.Sprintf("k%d", i), i)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = c.Get(fmt.Sprintf("k%d", i%1000))
			i++
		}
	})
}
func BenchmarkGetMiss(b *testing.B) {
	c := NewCaches[int](time.Minute, time.Minute)
	c.SetFindingCallback(func(key string) (int, bool) {
		return 42, true
	})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = c.Get(fmt.Sprintf("k%d", i))
			i++
		}
	})
}

// BenchmarkSet 基准测试：并发 Set。
func BenchmarkSet(b *testing.B) {
	c := NewCaches[int](time.Minute, time.Minute)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Set(fmt.Sprintf("k%d", i), i)
			i++
		}
	})
}

// BenchmarkRebuild 基准测试：不同规模的 Rebuild。
func BenchmarkRebuild(b *testing.B) {
	for _, size := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			c := NewCaches[int](time.Minute, time.Minute)
			data := make(map[string]int, size)
			for i := 0; i < size; i++ {
				data[fmt.Sprintf("k%d", i)] = i
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Rebuild(data)
			}
		})
	}
}
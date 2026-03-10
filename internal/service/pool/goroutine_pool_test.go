// Package pool 包含协程池核心逻辑及对应的单元测试
// 运行测试（含竞态检测）：go test -race ./pool -v
package pool

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	// 模拟业务日志包（测试中替换为打印）
	"kama_chat_server/pkg/zlog"
)

// ------------------------------
// 协程池核心实现（补充后测试才能运行）
// ------------------------------

// 错误定义
var (
	ErrPoolFull   = errors.New("goroutine pool task queue is full, task rejected")
	ErrPoolClosed = errors.New("goroutine pool is already closed")
)

// Task 协程池执行的任务类型
type Task func()

// Option 协程池创建选项
type Option func(*Pool)

// WithPanicHandler 自定义 panic 处理函数
func WithPanicHandler(h func(interface{})) Option {
	return func(p *Pool) { p.panicHandler = h }
}

// poolMetrics 协程池运行指标（原子操作）
type poolMetrics struct {
	submitted  int64 // 累计提交任务数
	processing int64 // 正在执行的任务数
	completed  int64 // 累计完成任务数
	rejected   int64 // 累计拒绝任务数
}

// Pool 固定大小协程池
type Pool struct {
	size         int               // worker 数量
	taskQueue    chan Task         // 任务队列（有界）
	wg           sync.WaitGroup    // 等待 worker 退出
	once         sync.Once         // 保证 Shutdown 只执行一次
	closed       int32             // 0=运行中, 1=已关闭
	metrics      poolMetrics       // 运行指标
	panicHandler func(interface{}) // panic 处理函数
}

// PoolStats 协程池状态快照
type PoolStats struct {
	Workers    int   `json:"workers"`     // 固定 worker 数（对应测试中的 WorkerSize）
	QueueCap   int   `json:"queue_cap"`   // 队列容量
	QueueDepth int   `json:"queue_depth"` // 队列当前任务数
	Submitted  int64 `json:"submitted"`   // 累计提交
	Processing int64 `json:"processing"`  // 执行中
	Completed  int64 `json:"completed"`   // 累计完成
	Rejected   int64 `json:"rejected"`    // 累计拒绝
	IsClosed   bool  `json:"is_closed"`   // 是否关闭
}

// NewPool 创建协程池并启动 worker
func NewPool(size, queueSize int, opts ...Option) *Pool {
	// 参数保底
	if size <= 0 {
		size = 1
	}
	if queueSize <= 0 {
		queueSize = size * 10
	}

	p := &Pool{
		size:      size,
		taskQueue: make(chan Task, queueSize),
		// 默认 panic 处理器
		panicHandler: func(r interface{}) {
			zlog.Error(fmt.Sprintf("[goroutine-pool] panic recovered: %v\n%s", r, debug.Stack()))
		},
	}

	// 应用自定义选项
	for _, o := range opts {
		o(p)
	}

	// 启动 worker
	p.startWorkers()

	return p
}

// startWorkers 启动所有 worker
func (p *Pool) startWorkers() {
	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.worker()
	}
}

// worker 消费任务队列
func (p *Pool) worker() {
	defer p.wg.Done()

	for task := range p.taskQueue {
		atomic.AddInt64(&p.metrics.processing, 1)
		p.safeRun(task)
		atomic.AddInt64(&p.metrics.processing, -1)
		atomic.AddInt64(&p.metrics.completed, 1)
	}
}

// safeRun 执行任务并捕获 panic
func (p *Pool) safeRun(task Task) {
	defer func() {
		if r := recover(); r != nil {
			p.panicHandler(r)
		}
	}()
	task()
}

// Submit 非阻塞提交任务
func (p *Pool) Submit(task Task) error {
	if atomic.LoadInt32(&p.closed) == 1 {
		atomic.AddInt64(&p.metrics.rejected, 1)
		return ErrPoolClosed
	}

	atomic.AddInt64(&p.metrics.submitted, 1)
	select {
	case p.taskQueue <- task:
		return nil
	default:
		atomic.AddInt64(&p.metrics.rejected, 1)
		return ErrPoolFull
	}
}

// SubmitWithContext 带上下文提交任务
func (p *Pool) SubmitWithContext(ctx context.Context, task Task) error {
	if atomic.LoadInt32(&p.closed) == 1 {
		atomic.AddInt64(&p.metrics.rejected, 1)
		return ErrPoolClosed
	}

	atomic.AddInt64(&p.metrics.submitted, 1)
	select {
	case p.taskQueue <- task:
		return nil
	case <-ctx.Done():
		atomic.AddInt64(&p.metrics.rejected, 1)
		return ctx.Err()
	}
}

// Shutdown 优雅关闭协程池
func (p *Pool) Shutdown() {
	p.once.Do(func() {
		atomic.StoreInt32(&p.closed, 1)
		close(p.taskQueue)
		p.wg.Wait()
	})
}

// ShutdownWithTimeout 带超时关闭
func (p *Pool) ShutdownWithTimeout(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		p.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Stats 获取协程池状态快照
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Workers:    p.size,
		QueueCap:   cap(p.taskQueue),
		QueueDepth: len(p.taskQueue),
		Submitted:  atomic.LoadInt64(&p.metrics.submitted),
		Processing: atomic.LoadInt64(&p.metrics.processing),
		Completed:  atomic.LoadInt64(&p.metrics.completed),
		Rejected:   atomic.LoadInt64(&p.metrics.rejected),
		IsClosed:   atomic.LoadInt32(&p.closed) == 1,
	}
}

// ------------------------------
// 全局 AIWorkerPool 单例（测试用）
// ------------------------------
const (
	aiPoolWorkers   = 20
	aiPoolQueueSize = 200
)

var AIWorkerPool *Pool

func init() {
	AIWorkerPool = NewPool(aiPoolWorkers, aiPoolQueueSize)
}

// ------------------------------
// 测试用例（修复语法错误，完善逻辑）
// ------------------------------

// TestNewPool_BasicCreation 验证 NewPool 在正常参数下成功创建并启动
func TestNewPool_BasicCreation(t *testing.T) {
	p := NewPool(4, 16)
	defer p.Shutdown()

	// 断言 worker 数和队列容量
	stats := p.Stats()
	if stats.Workers != 4 {
		t.Errorf("worker size = %d, want 4", stats.Workers)
	}
	if stats.QueueCap != 16 {
		t.Errorf("queue cap = %d, want 16", stats.QueueCap)
	}
	if stats.IsClosed {
		t.Error("pool should not be closed after creation")
	}
}

// TestPool_Submit_TaskExecution 验证提交的任务被全部执行
func TestPool_Submit_TaskExecution(t *testing.T) {
	const taskCount = 50
	p := NewPool(4, 100)
	defer p.Shutdown()

	var (
		executed int64
		wg       sync.WaitGroup
	)

	// 提交任务
	for i := 0; i < taskCount; i++ {
		wg.Add(1)
		err := p.Submit(func() {
			defer wg.Done()
			atomic.AddInt64(&executed, 1)
		})
		if err != nil {
			t.Fatalf("Submit returned unexpected error: %v", err)
		}
	}

	// 等待所有任务完成
	wg.Wait()

	// 断言任务全部执行
	if executed != taskCount {
		t.Errorf("executed = %d, want %d", executed, taskCount)
	}
}

// TestPool_Submit_QueueFull_ReturnsErrPoolFull 验证队列满时返回 ErrPoolFull
func TestPool_Submit_QueueFull_ReturnsErrPoolFull(t *testing.T) {
	// 1 个 worker + 队列容量 1，确保能触发队列满
	p := NewPool(1, 1)
	defer p.Shutdown()

	// 阻塞 worker（让队列占满）
	block := make(chan struct{})
	_ = p.Submit(func() { <-block })
	time.Sleep(10 * time.Millisecond) // 等待 worker 取走任务并阻塞

	// 占满队列
	_ = p.Submit(func() {})

	// 再次提交，应返回 ErrPoolFull
	err := p.Submit(func() {})
	if err != ErrPoolFull {
		t.Errorf("expected ErrPoolFull, got %v", err)
	}

	// 释放阻塞的 worker
	close(block)
}

// TestPool_Submit_AfterShutdown_ReturnsErrPoolClosed 验证关闭后提交返回 ErrPoolClosed
func TestPool_Submit_AfterShutdown_ReturnsErrPoolClosed(t *testing.T) {
	p := NewPool(2, 10)
	p.Shutdown() // 关闭池

	// 提交任务，应返回 ErrPoolClosed
	err := p.Submit(func() {})
	if err != ErrPoolClosed {
		t.Errorf("expected ErrPoolClosed, got %v", err)
	}
}

// TestPool_SubmitWithContext_ContextCancel 验证 ctx 取消时 SubmitWithContext 立即返回
func TestPool_SubmitWithContext_ContextCancel(t *testing.T) {
	// 1 个 worker + 队列容量 1，制造阻塞
	p := NewPool(1, 1)
	defer p.Shutdown()

	// 阻塞 worker 并占满队列
	block := make(chan struct{})
	_ = p.Submit(func() { <-block })
	time.Sleep(10 * time.Millisecond)
	_ = p.Submit(func() {})

	// 带超时上下文提交任务
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.SubmitWithContext(ctx, func() {})

	// 断言返回上下文错误
	if err == nil {
		t.Error("expected context error, got nil")
	}

	// 释放阻塞的 worker
	close(block)
}

// TestPool_PanicRecovery 验证任务内 panic 被捕获，其他任务正常运行
func TestPool_PanicRecovery(t *testing.T) {
	var (
		panicCaught int64
		normalDone  int64
		wg          sync.WaitGroup
	)

	// 自定义 panic 处理器
	p := NewPool(2, 20, WithPanicHandler(func(r interface{}) {
		atomic.AddInt64(&panicCaught, 1)
	}))
	defer p.Shutdown()

	// 提交会 panic 的任务
	_ = p.Submit(func() {
		panic("intentional test panic")
	})

	// 提交正常任务
	wg.Add(1)
	_ = p.Submit(func() {
		defer wg.Done()
		atomic.AddInt64(&normalDone, 1)
	})

	// 等待任务执行完成
	wg.Wait()
	time.Sleep(50 * time.Millisecond) // 给 panic 处理器异步执行时间

	// 断言 panic 被捕获
	if atomic.LoadInt64(&panicCaught) == 0 {
		t.Error("panic handler was not called")
	}

	// 断言正常任务执行
	if normalDone != 1 {
		t.Error("normal task after panic was not executed")
	}
}

// TestPool_ConcurrentSafety 验证多 goroutine 并发 Submit 的正确性（race detector 检测）
func TestPool_ConcurrentSafety(t *testing.T) {
	const total = 400
	p := NewPool(8, 500)
	defer p.Shutdown()

	var (
		counter int64
		wg      sync.WaitGroup
	)

	// 并发提交任务
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			_ = p.Submit(func() {
				defer wg.Done()
				atomic.AddInt64(&counter, 1)
			})
		}()
	}

	// 等待所有任务完成
	wg.Wait()

	// 断言所有任务都执行
	got := atomic.LoadInt64(&counter)
	if got != total {
		t.Errorf("concurrent counter = %d, want %d", got, total)
	}
}

// TestPool_Stats_Accuracy 验证 Stats() 指标数据的准确性
func TestPool_Stats_Accuracy(t *testing.T) {
	const taskCount = 20
	p := NewPool(2, 50)

	var wg sync.WaitGroup
	// 提交任务
	for i := 0; i < taskCount; i++ {
		wg.Add(1)
		_ = p.Submit(func() {
			defer wg.Done()
			time.Sleep(time.Millisecond) // 确保任务有执行时间
		})
	}

	// 等待任务完成并关闭池
	wg.Wait()
	p.Shutdown()

	// 获取状态并断言
	stats := p.Stats()
	if stats.Completed != int64(taskCount) {
		t.Errorf("Stats.Completed = %d, want %d", stats.Completed, taskCount)
	}
	if stats.Submitted < int64(taskCount) {
		t.Errorf("Stats.Submitted = %d, want >= %d", stats.Submitted, taskCount)
	}
}

// TestPool_Shutdown_Idempotent 验证多次调用 Shutdown 是安全的
func TestPool_Shutdown_Idempotent(t *testing.T) {
	p := NewPool(2, 10)
	// 多次调用 Shutdown（应无副作用）
	p.Shutdown()
	p.Shutdown()

	// 断言池已关闭
	stats := p.Stats()
	if !stats.IsClosed {
		t.Error("pool should be closed after Shutdown")
	}
}

// TestPool_ShutdownWithTimeout 验证带超时关闭
func TestPool_ShutdownWithTimeout(t *testing.T) {
	p := NewPool(2, 10)

	var wg sync.WaitGroup
	// 提交短任务
	for i := 0; i < 5; i++ {
		wg.Add(1)
		_ = p.Submit(func() {
			defer wg.Done()
			time.Sleep(5 * time.Millisecond)
		})
	}

	// 带超时关闭（应优雅完成）
	graceful := p.ShutdownWithTimeout(2 * time.Second)
	if !graceful {
		t.Error("ShutdownWithTimeout should return true for fast tasks")
	}
}

// TestAIWorkerPool_GlobalSingleton 验证全局 AIWorkerPool 单例已初始化
func TestAIWorkerPool_GlobalSingleton(t *testing.T) {
	// 断言单例非空
	if AIWorkerPool == nil {
		t.Fatal("AIWorkerPool global singleton is nil")
	}

	var (
		done int64
		wg   sync.WaitGroup
	)

	// 提交任务测试单例是否可用
	wg.Add(1)
	err := AIWorkerPool.Submit(func() {
		defer wg.Done()
		atomic.StoreInt64(&done, 1)
	})
	if err != nil {
		t.Skipf("AIWorkerPool.Submit error (pool may be closed): %v", err)
	}

	// 等待任务完成
	wg.Wait()

	// 断言任务执行
	if done != 1 {
		t.Error("task submitted to AIWorkerPool was not executed")
	}
}

// Package pool 提供固定大小协程池，用于隔离 AI 大模型耗时请求，
// 避免直接 go func() 导致协程数量失控、阻塞主聊天流程。
//
// 设计目标：
//   - 固定 Worker 数量，防止 goroutine 爆炸
//   - 有界任务队列 + 背压控制（队满拒绝 or 带 Context 等待）
//   - 原子计数器实时追踪：已提交 / 处理中 / 已完成 / 已拒绝
//   - 一次性优雅关闭（sync.Once），等待所有在途任务完成
package pool

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"kama_chat_server/pkg/zlog"
)

// ──────────────────────────────────────────────
// 错误定义
// ──────────────────────────────────────────────
var (
	ErrPoolFull   = errors.New("goroutine pool task queue is full, task rejected")
	ErrPoolClosed = errors.New("goroutine pool is already closed")
)

// ──────────────────────────────────────────────
// Task & Option
// ──────────────────────────────────────────────

// Task 是池中执行的最小工作单元
type Task func()

// Option 用于创建时自定义池行为
type Option func(*Pool)

// WithPanicHandler 注册 panic 恢复回调，默认仅打印堆栈
func WithPanicHandler(h func(interface{})) Option {
	return func(p *Pool) { p.panicHandler = h }
}

// ──────────────────────────────────────────────
// 内部 metrics（原子操作，无锁）
// ──────────────────────────────────────────────
type poolMetrics struct {
	submitted  int64 // 累计提交任务数
	processing int64 // 当前正在执行的任务数
	completed  int64 // 累计完成任务数
	rejected   int64 // 累计拒绝任务数（队满 / 池关闭）
}

// ──────────────────────────────────────────────
// Pool
// ──────────────────────────────────────────────

// Pool 是一个固定大小的协程池，对外提供 Submit / SubmitWithContext / Shutdown 接口
type Pool struct {
	size         int               // 固定 worker 数量
	taskQueue    chan Task         // 任务队列（有界）
	wg           sync.WaitGroup    // 等待所有 worker 退出
	once         sync.Once         // 保证 Shutdown 只执行一次
	closed       int32             // 0=运行中, 1=已关闭（原子操作）
	metrics      poolMetrics       // 运行指标（原子更新）
	panicHandler func(interface{}) // panic 捕获回调
}

// NewPool 创建并立即启动池内的 size 个 worker goroutine
// queueSize 决定任务队列容量；当队列满且无可用 worker 时，Submit 返回 ErrPoolFull
func NewPool(size, queueSize int, opts ...Option) *Pool {
	// 保底参数：防止传入非法值
	if size <= 0 {
		size = 1
	}
	if queueSize <= 0 {
		queueSize = size * 10 // 默认队列容量 = worker数 * 10
	}

	// 初始化池对象
	p := &Pool{
		size:      size,
		taskQueue: make(chan Task, queueSize),
		// 默认 panic 处理器：打印错误和堆栈
		panicHandler: func(r interface{}) {
			zlog.Error(fmt.Sprintf("[goroutine-pool] panic recovered: %v\n%s", r, debug.Stack()))
		},
	}

	// 应用自定义配置
	for _, o := range opts {
		o(p)
	}

	// 启动所有 worker
	p.startWorkers()

	return p
}

// startWorkers 启动 size 个后台 worker
func (p *Pool) startWorkers() {
	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.worker()
	}
}

// worker 持续从 taskQueue 取任务执行，直到 channel 被关闭
func (p *Pool) worker() {
	defer p.wg.Done()

	// 循环消费任务，直到 taskQueue 被关闭
	for task := range p.taskQueue {
		// 更新运行指标：开始处理
		atomic.AddInt64(&p.metrics.processing, 1)

		// 安全执行任务（捕获 panic）
		p.safeRun(task)

		// 更新运行指标：完成处理
		atomic.AddInt64(&p.metrics.processing, -1)
		atomic.AddInt64(&p.metrics.completed, 1)
	}
}

// safeRun 包裹 task 执行，捕获 panic 防止 worker 崩溃
func (p *Pool) safeRun(task Task) {
	defer func() {
		if r := recover(); r != nil {
			p.panicHandler(r)
		}
	}()
	task()
}

// Submit 非阻塞提交任务。若队满或池已关闭，立即返回错误
func (p *Pool) Submit(task Task) error {
	// 检查池状态：已关闭则拒绝
	if atomic.LoadInt32(&p.closed) == 1 {
		atomic.AddInt64(&p.metrics.rejected, 1)
		return ErrPoolClosed
	}

	// 累计提交数 +1
	atomic.AddInt64(&p.metrics.submitted, 1)

	// 非阻塞入队：队满则立即返回错误
	select {
	case p.taskQueue <- task:
		return nil
	default:
		atomic.AddInt64(&p.metrics.rejected, 1)
		return ErrPoolFull
	}
}

// SubmitWithContext 阻塞等待直到任务被入队、ctx 超时或池关闭
// 适用于调用方愿意短暂等待可用队列位置的场景
func (p *Pool) SubmitWithContext(ctx context.Context, task Task) error {
	// 检查池状态：已关闭则拒绝
	if atomic.LoadInt32(&p.closed) == 1 {
		atomic.AddInt64(&p.metrics.rejected, 1)
		return ErrPoolClosed
	}

	// 累计提交数 +1
	atomic.AddInt64(&p.metrics.submitted, 1)

	// 带上下文的阻塞入队
	select {
	case p.taskQueue <- task:
		return nil
	case <-ctx.Done():
		atomic.AddInt64(&p.metrics.rejected, 1)
		return ctx.Err()
	}
}

// Shutdown 优雅关闭池。关闭 taskQueue channel，等待所有在途任务执行完毕
// 多次调用是安全的（sync.Once 保证只关闭一次）
func (p *Pool) Shutdown() {
	p.once.Do(func() {
		// 标记池为已关闭
		atomic.StoreInt32(&p.closed, 1)
		// 关闭任务队列：worker 会在消费完现有任务后退出
		close(p.taskQueue)
		// 等待所有 worker 退出
		p.wg.Wait()
		zlog.Info("[goroutine-pool] all workers exited gracefully")
	})
}

// ShutdownWithTimeout 带超时的关闭，超时后忽略未执行完的任务并返回 false
func (p *Pool) ShutdownWithTimeout(timeout time.Duration) bool {
	done := make(chan struct{})

	// 异步执行优雅关闭
	go func() {
		p.Shutdown()
		close(done)
	}()

	// 等待关闭完成或超时
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ──────────────────────────────────────────────
// Stats / 可观测性
// ──────────────────────────────────────────────

// PoolStats 是一次快照，包含当前池状态与累计指标
type PoolStats struct {
	Workers    int   `json:"workers"`     // 固定 worker 数
	QueueCap   int   `json:"queue_cap"`   // 任务队列容量
	QueueDepth int   `json:"queue_depth"` // 当前队列积压任务数
	Submitted  int64 `json:"submitted"`   // 累计提交
	Processing int64 `json:"processing"`  // 执行中
	Completed  int64 `json:"completed"`   // 累计完成
	Rejected   int64 `json:"rejected"`    // 累计拒绝
	IsClosed   bool  `json:"is_closed"`   // 池是否已关闭
}

// Stats 返回当前池的运行快照，无锁、可并发安全读取
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

// ──────────────────────────────────────────────
// 全局 AI 协程池（单例，供 chat.Server 直接使用）
// ──────────────────────────────────────────────
const (
	aiPoolWorkers   = 20  // 并发 AI worker 数量
	aiPoolQueueSize = 200 // AI 任务队列容量
)

// AIWorkerPool 全局 AI 协程池单例
var AIWorkerPool *Pool

// init 初始化全局 AI 协程池
func init() {
	AIWorkerPool = NewPool(aiPoolWorkers, aiPoolQueueSize)
	zlog.Info(fmt.Sprintf(
		"[goroutine-pool] AI worker pool started: workers=%d, queue=%d",
		aiPoolWorkers, aiPoolQueueSize,
	))
}

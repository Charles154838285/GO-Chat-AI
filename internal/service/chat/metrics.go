// Package chat — metrics.go
// 提供 WebSocket 连接与消息的实时可观测指标，基于原子操作实现无锁统计。
//
// 统计维度：
//   - 活跃长连接数 / 历史总连接数
//   - 消息收发累计 QPS（滑动窗口 1s 内计数）
//   - AI 请求总量 / 失败量
//   - 服务运行时长
//
// 用途：通过 GET /metrics 接口对外暴露，辅助压测验证和性能分析。
package chat

import (
	"sync"
	"sync/atomic"
	"time"
)

// ──────────────────────────────────────────────
// 核心指标结构
// ──────────────────────────────────────────────

// wsMetrics 存放所有 WebSocket 级别的运行指标
type wsMetrics struct {
	activeConns  int64 // 当前活跃 WebSocket 连接数
	totalConns   int64 // 历史累计建立连接数
	msgsReceived int64 // 累计接收消息数
	msgsSent     int64 // 累计发送消息数
	aiTotal      int64 // AI 请求总数
	aiFailed     int64 // AI 请求失败数
	startTime    time.Time

	// QPS 滑动窗口（1 秒重置）
	qpsMu         sync.Mutex
	qpsWindow     [2]int64  // [当前桶, 上一桶]
	qpsWindowTime time.Time // 当前桶起始时刻
}

// connMetrics 是全局单例指标对象，在 init 中初始化。
var connMetrics = &wsMetrics{
	startTime:     time.Now(),
	qpsWindowTime: time.Now(),
}

// ──────────────────────────────────────────────
// 事件钩子（由 Server/Client 调用）
// ──────────────────────────────────────────────

// OnConnect 在 WebSocket 连接建立时调用。
func (m *wsMetrics) OnConnect() {
	atomic.AddInt64(&m.activeConns, 1)
	atomic.AddInt64(&m.totalConns, 1)
}

// OnDisconnect 在 WebSocket 连接断开时调用。
func (m *wsMetrics) OnDisconnect() {
	atomic.AddInt64(&m.activeConns, -1)
}

// OnMessageReceived 每收到一条 WebSocket 消息时调用。
func (m *wsMetrics) OnMessageReceived() {
	atomic.AddInt64(&m.msgsReceived, 1)
	m.recordQPS()
}

// OnMessageSent 每成功发送一条 WebSocket 消息时调用。
func (m *wsMetrics) OnMessageSent() {
	atomic.AddInt64(&m.msgsSent, 1)
}

// OnAIRequest 在 AI 请求结束时调用；failed=true 表示本次调用失败。
func (m *wsMetrics) OnAIRequest(failed bool) {
	atomic.AddInt64(&m.aiTotal, 1)
	if failed {
		atomic.AddInt64(&m.aiFailed, 1)
	}
}

// recordQPS 维护一个 1 秒滑动窗口计数器。
func (m *wsMetrics) recordQPS() {
	m.qpsMu.Lock()
	defer m.qpsMu.Unlock()
	now := time.Now()
	if now.Sub(m.qpsWindowTime) >= time.Second {
		// 滚动窗口：当前桶 → 上一桶
		m.qpsWindow[1] = m.qpsWindow[0]
		m.qpsWindow[0] = 0
		m.qpsWindowTime = now
	}
	m.qpsWindow[0]++
}

// currentQPS 返回上一个完整 1 秒窗口的 QPS（更精确）。
func (m *wsMetrics) currentQPS() int64 {
	m.qpsMu.Lock()
	defer m.qpsMu.Unlock()
	return m.qpsWindow[1]
}

// ──────────────────────────────────────────────
// 对外快照
// ──────────────────────────────────────────────

// MetricsSnapshot 是导出给 HTTP /metrics 接口的完整指标结构。
type MetricsSnapshot struct {
	ActiveConnections int64   `json:"active_connections"` // 当前在线长连接数
	TotalConnections  int64   `json:"total_connections"`  // 历史总连接数
	MessagesReceived  int64   `json:"messages_received"`  // 累计收到消息数
	MessagesSent      int64   `json:"messages_sent"`      // 累计发出消息数
	CurrentQPS        int64   `json:"current_qps"`        // 当前消息处理 QPS
	AIRequestsTotal   int64   `json:"ai_requests_total"`  // AI 请求总数
	AIRequestsFailed  int64   `json:"ai_requests_failed"` // AI 请求失败数
	UptimeSeconds     float64 `json:"uptime_seconds"`     // 服务运行时长（秒）
}

// GetMetricsSnapshot 返回一次无锁快照，可直接序列化为 JSON 对外暴露。
func GetMetricsSnapshot() MetricsSnapshot {
	return MetricsSnapshot{
		ActiveConnections: atomic.LoadInt64(&connMetrics.activeConns),
		TotalConnections:  atomic.LoadInt64(&connMetrics.totalConns),
		MessagesReceived:  atomic.LoadInt64(&connMetrics.msgsReceived),
		MessagesSent:      atomic.LoadInt64(&connMetrics.msgsSent),
		CurrentQPS:        connMetrics.currentQPS(),
		AIRequestsTotal:   atomic.LoadInt64(&connMetrics.aiTotal),
		AIRequestsFailed:  atomic.LoadInt64(&connMetrics.aiFailed),
		UptimeSeconds:     time.Since(connMetrics.startTime).Seconds(),
	}
}

// GetActiveConnections 返回当前活跃连接数（供 Server.Start 打印日志使用）。
func GetActiveConnections() int64 {
	return atomic.LoadInt64(&connMetrics.activeConns)
}

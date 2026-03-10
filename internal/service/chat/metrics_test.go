package chat

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestOnConnect_IncrementsActiveAndTotal 验证 OnConnect 同时增加活跃连接数和历史总数
func TestOnConnect_IncrementsActiveAndTotal(t *testing.T) {
	m := &wsMetrics{startTime: time.Now(), qpsWindowTime: time.Now()}

	m.OnConnect()
	m.OnConnect()

	if got := atomic.LoadInt64(&m.activeConns); got != 2 {
		t.Errorf("activeConns = %d, want 2", got)
	}
	if got := atomic.LoadInt64(&m.totalConns); got != 2 {
		t.Errorf("totalConns = %d, want 2", got)
	}
}

// TestOnDisconnect_DecrementsActiveConns 验证断开连接后活跃连接数减少，历史总数不变
func TestOnDisconnect_DecrementsActiveConns(t *testing.T) {
	m := &wsMetrics{startTime: time.Now(), qpsWindowTime: time.Now()}

	m.OnConnect()
	m.OnConnect()
	m.OnDisconnect()

	if got := atomic.LoadInt64(&m.activeConns); got != 1 {
		t.Errorf("activeConns after disconnect = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&m.totalConns); got != 2 {
		t.Errorf("totalConns after disconnect = %d, want 2 (unchanged)", got)
	}
}

// TestOnMessageReceived_IncrementsCounter 验证消息接收计数器正确递增
func TestOnMessageReceived_IncrementsCounter(t *testing.T) {
	m := &wsMetrics{startTime: time.Now(), qpsWindowTime: time.Now()}

	const count = 100
	for i := 0; i < count; i++ {
		m.OnMessageReceived()
	}

	if got := atomic.LoadInt64(&m.msgsReceived); got != count {
		t.Errorf("msgsReceived = %d, want %d", got, count)
	}
}

// TestOnMessageSent_IncrementsCounter 验证消息发送计数器正确递增
func TestOnMessageSent_IncrementsCounter(t *testing.T) {
	m := &wsMetrics{startTime: time.Now(), qpsWindowTime: time.Now()}

	const count = 50
	for i := 0; i < count; i++ {
		m.OnMessageSent()
	}

	if got := atomic.LoadInt64(&m.msgsSent); got != count {
		t.Errorf("msgsSent = %d, want %d", got, count)
	}
}

// TestOnAIRequest_CountsSuccessAndFailure 验证 AI 请求计数区分成功和失败
func TestOnAIRequest_CountsSuccessAndFailure(t *testing.T) {
	m := &wsMetrics{startTime: time.Now(), qpsWindowTime: time.Now()}

	m.OnAIRequest(false)
	m.OnAIRequest(false)
	m.OnAIRequest(true)

	if got := atomic.LoadInt64(&m.aiTotal); got != 3 {
		t.Errorf("aiTotal = %d, want 3", got)
	}
	if got := atomic.LoadInt64(&m.aiFailed); got != 1 {
		t.Errorf("aiFailed = %d, want 1", got)
	}
}

// TestGetMetricsSnapshot_GlobalMetrics 验证全局 GetMetricsSnapshot 函数返回合法快照
func TestGetMetricsSnapshot_GlobalMetrics(t *testing.T) {
	before := GetActiveConnections()
	connMetrics.OnConnect()
	connMetrics.OnConnect()
	connMetrics.OnDisconnect()
	connMetrics.OnMessageReceived()
	connMetrics.OnMessageSent()
	connMetrics.OnAIRequest(false)
	connMetrics.OnAIRequest(true)

	snap := GetMetricsSnapshot()

	// 至少比操作前多了 1 条连接
	if snap.ActiveConnections < before+1 {
		t.Errorf("ActiveConnections = %d, want >= %d", snap.ActiveConnections, before+1)
	}
	if snap.MessagesReceived < 1 {
		t.Errorf("MessagesReceived = %d, want >= 1", snap.MessagesReceived)
	}
	if snap.MessagesSent < 1 {
		t.Errorf("MessagesSent = %d, want >= 1", snap.MessagesSent)
	}
	if snap.AIRequestsTotal < 2 {
		t.Errorf("AIRequestsTotal = %d, want >= 2", snap.AIRequestsTotal)
	}
	if snap.AIRequestsFailed < 1 {
		t.Errorf("AIRequestsFailed = %d, want >= 1", snap.AIRequestsFailed)
	}
	if snap.UptimeSeconds <= 0 {
		t.Errorf("UptimeSeconds = %f, want > 0", snap.UptimeSeconds)
	}

	// 清理：断开额外的连接
	connMetrics.OnDisconnect()
}

// TestGetActiveConnections_Delta 验证 GetActiveConnections 精确反映当前活跃数变化
func TestGetActiveConnections_Delta(t *testing.T) {
	before := GetActiveConnections()
	connMetrics.OnConnect()
	connMetrics.OnConnect()
	connMetrics.OnConnect()

	if got := GetActiveConnections(); got != before+3 {
		t.Errorf("GetActiveConnections = %d, want %d", got, before+3)
	}

	connMetrics.OnDisconnect()
	connMetrics.OnDisconnect()

	if got := GetActiveConnections(); got != before+1 {
		t.Errorf("GetActiveConnections after 2 disconnects = %d, want %d", got, before+1)
	}

	// 清理
	connMetrics.OnDisconnect()
}

// TestMetrics_ConcurrentSafety race detector 验证原子操作的线程安全性
func TestMetrics_ConcurrentSafety(t *testing.T) {
	m := &wsMetrics{startTime: time.Now(), qpsWindowTime: time.Now()}

	const workers = 100
	const ops = 50

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				m.OnConnect()
				m.OnMessageReceived()
				m.OnMessageSent()
				m.OnAIRequest(j%5 == 0)
				if j%2 == 0 {
					m.OnDisconnect()
				}
			}
		}(i)
	}

	wg.Wait()

	// 活跃连接数不应为负
	if got := atomic.LoadInt64(&m.activeConns); got < 0 {
		t.Errorf("activeConns went negative: %d", got)
	}
}

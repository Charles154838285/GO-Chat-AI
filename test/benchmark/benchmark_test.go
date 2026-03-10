// Package benchmark 提供 WebSocket 高并发压测和 QPS 基准测试
// 运行方式：
//
//	go test ./test/benchmark/ -v -run TestWebSocketConcurrency   # 并发连接数压测
//	go test ./test/benchmark/ -v -run TestMessageQPS             # 消息 QPS 测试
//	go test ./test/benchmark/ -bench=. -benchtime=10s            # Go 标准 Benchmark 模式
package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 常量定义：测试配置 & 压测目标
const (
	// 测试目标服务器地址（需先启动服务）
	serverAddr = "127.0.0.1:8000"
	wsPath     = "/ws"

	// 压测目标
	targetConcurrentConns = 1000 // 目标并发长连接数
	targetQPS             = 5000 // 目标单聊消息 QPS
)

// chatMessage 定义 WebSocket 消息结构，与服务端 MessageRequest 对齐
type chatMessage struct {
	Type        int    `json:"type"`
	SenderID    string `json:"sender_id"`
	RecipientID string `json:"recipient_id"`
	Content     string `json:"content"`
	ContentType int    `json:"content_type"`
	SessionID   string `json:"session_id"`
}

// dialWS 建立一条 WebSocket 连接（工具函数）
func dialWS(clientID string) (*websocket.Conn, error) {
	// 构造 WebSocket 连接 URL
	u := url.URL{
		Scheme:   "ws",
		Host:     serverAddr,
		Path:     wsPath,
		RawQuery: "client_id=" + clientID,
	}

	// 配置拨号器（设置握手超时）
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	// 建立连接
	conn, _, err := dialer.Dial(u.String(), http.Header{})
	return conn, err
}

// ----- 并发连接压测 -----

// TestWebSocketConcurrency 验证服务端能够稳定承载 1000+ 并发 WebSocket 长连接
// 测试策略：
//  1. 并发 goroutine 批量建立连接，记录成功 / 失败数
//  2. 成功建立后保持连接 holdTime，模拟真实长连接场景
//  3. 统计最终成功率，要求 ≥ 95%
func TestWebSocketConcurrency(t *testing.T) {
	// 配置
	total := targetConcurrentConns // 总连接数
	holdTime := 5 * time.Second    // 连接保持时长
	var (
		successCount int64          // 成功建立连接数
		failCount    int64          // 失败连接数
		wgDial       sync.WaitGroup // 等待所有连接建立完成
		wgHold       sync.WaitGroup // 等待连接保持时长结束
		connMu       sync.Mutex     // 保护 conns 切片的并发访问
	)

	// 初始化连接切片（存储所有成功的连接）
	conns := make([]*websocket.Conn, total)

	t.Logf("开始建立 %d 条并发 WebSocket 连接...", total)
	start := time.Now()

	// 并发建立连接
	for i := 0; i < total; i++ {
		wgDial.Add(1)
		go func(idx int) {
			defer wgDial.Done()

			// 生成唯一客户端 ID
			clientID := fmt.Sprintf("bench_user_%d_%d", idx, time.Now().UnixNano())
			// 建立连接
			conn, err := dialWS(clientID)

			if err != nil {
				atomic.AddInt64(&failCount, 1)
				return
			}

			// 连接成功：记录到切片
			atomic.AddInt64(&successCount, 1)
			connMu.Lock()
			conns[idx] = conn
			connMu.Unlock()
		}(i)
	}

	// 等待所有连接建立完成
	wgDial.Wait()
	dialDuration := time.Since(start)
	t.Logf("连接建立完毕：成功=%d 失败=%d 耗时=%v", successCount, failCount, dialDuration)

	// 保持连接指定时长（模拟长连接）
	wgHold.Add(1)
	go func() {
		defer wgHold.Done()
		time.Sleep(holdTime)
	}()
	wgHold.Wait()

	// 关闭所有连接
	connMu.Lock()
	for _, c := range conns {
		if c != nil {
			// 发送关闭消息并关闭连接
			_ = c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			c.Close()
		}
	}
	connMu.Unlock()

	// 统计成功率
	successRate := float64(successCount) / float64(total) * 100
	t.Logf("并发连接成功率: %.2f%% (%d/%d)", successRate, successCount, total)

	// 验证成功率是否达标
	if successRate < 95 {
		t.Errorf("并发连接成功率 %.2f%% 低于目标 95%%（目标: %d 条并发）", successRate, targetConcurrentConns)
	} else {
		t.Logf("PASS: 成功支撑 %d+ 并发长连接", int(successCount))
	}
}

// ----- 消息 QPS 压测 -----

// TestMessageQPS 验证单聊消息处理 QPS ≥ 5000
// 测试策略：
//  1. 建立 N 条 WebSocket 连接
//  2. 在 testDuration 内每条连接持续发送消息
//  3. 统计成功发送总数并计算 QPS
func TestMessageQPS(t *testing.T) {
	// 配置
	const (
		senderCount  = 50               // 并发发送方连接数
		testDuration = 10 * time.Second // 压测时长
	)

	var (
		totalSent int64          // 成功发送消息数
		totalFail int64          // 失败发送消息数
		wg        sync.WaitGroup // 等待所有发送协程结束
	)

	// 步骤1：建立所有发送连接
	sendConns := make([]*websocket.Conn, 0, senderCount)
	for i := 0; i < senderCount; i++ {
		clientID := fmt.Sprintf("qps_sender_%d_%d", i, time.Now().UnixNano())
		c, err := dialWS(clientID)
		if err != nil {
			t.Logf("建立连接 %d 失败: %v", i, err)
			continue
		}
		sendConns = append(sendConns, c)
	}

	// 无可用连接则跳过测试
	if len(sendConns) == 0 {
		t.Skip("无法建立任何 WebSocket 连接，跳过 QPS 测试（请先启动服务）")
	}
	t.Logf("成功建立 %d 条发送连接，开始 %v QPS 测试...", len(sendConns), testDuration)

	// 构造测试消息
	msg := chatMessage{
		Type:        1, // 单聊消息
		SenderID:    "qps_test_sender",
		RecipientID: "qps_test_receiver",
		Content:     "benchmark test message",
		ContentType: 1,
		SessionID:   "bench_session_001",
	}
	// 序列化消息（处理错误）
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("消息序列化失败: %v", err)
	}

	// 步骤2：并发发送消息
	ctx, cancel := context.WithTimeout(context.Background(), testDuration)
	defer cancel()

	start := time.Now()
	for _, c := range sendConns {
		wg.Add(1)
		go func(conn *websocket.Conn) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done(): // 压测时长结束
					return
				default:
					// 发送消息
					err := conn.WriteMessage(websocket.TextMessage, msgBytes)
					if err != nil {
						atomic.AddInt64(&totalFail, 1)
						return
					}
					atomic.AddInt64(&totalSent, 1)
				}
			}
		}(c)
	}

	// 等待所有发送协程结束
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	qps := float64(totalSent) / elapsed

	// 步骤3：关闭所有连接
	for _, c := range sendConns {
		c.Close()
	}

	// 输出结果 & 验证 QPS
	t.Logf("消息 QPS 测试结果: 发送总数=%d 失败=%d 耗时=%.2fs QPS=%.0f", totalSent, totalFail, elapsed, qps)
	if qps < float64(targetQPS) {
		t.Errorf("消息 QPS %.0f 低于目标 %d", qps, targetQPS)
	} else {
		t.Logf("PASS: 单聊消息处理 QPS=%.0f（目标: %d+）", qps, targetQPS)
	}
}

// ----- Go 标准 Benchmark -----

// BenchmarkWebSocketDial 基准测试：单条 WebSocket 连接建立耗时
func BenchmarkWebSocketDial(b *testing.B) {
	b.ResetTimer() // 重置计时器，排除初始化耗时

	// 循环建立连接
	for i := 0; i < b.N; i++ {
		clientID := fmt.Sprintf("bench_%d_%d", i, time.Now().UnixNano())
		conn, err := dialWS(clientID)
		if err != nil {
			b.Logf("连接失败（服务未启动则跳过）: %v", err)
			continue
		}
		conn.Close()
	}
}

// BenchmarkWebSocketSend 基准测试：单连接上 TextMessage 发送吞吐量
func BenchmarkWebSocketSend(b *testing.B) {
	// 建立测试连接
	clientID := fmt.Sprintf("bench_send_%d", time.Now().UnixNano())
	conn, err := dialWS(clientID)
	if err != nil {
		b.Skip("无法建立 WebSocket 连接，跳过（请先启动服务）")
	}
	defer conn.Close()

	// 构造测试消息并序列化
	msg := chatMessage{
		Type:        1,
		SenderID:    "bench_sender",
		RecipientID: "bench_receiver",
		Content:     "hello benchmark",
		ContentType: 1,
		SessionID:   "bench_session",
	}
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		b.Fatalf("消息序列化失败: %v", err)
	}

	// 重置计时器 + 开启内存分配统计
	b.ResetTimer()
	b.ReportAllocs()

	// 循环发送消息
	for i := 0; i < b.N; i++ {
		if err := conn.WriteMessage(websocket.TextMessage, msgBytes); err != nil {
			b.Fatalf("发送失败: %v", err)
		}
	}
}

// BenchmarkConcurrentConnections 基准测试：并发连接建立（模拟流量洪峰）
func BenchmarkConcurrentConnections(b *testing.B) {
	// 设置并发数
	b.SetParallelism(100)
	b.ResetTimer()

	// 并发建立连接
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			clientID := fmt.Sprintf("parallel_bench_%d_%d", i, time.Now().UnixNano())
			conn, err := dialWS(clientID)
			if err == nil {
				conn.Close()
			}
			i++
		}
	})
}

// Package ai — redis_session_manager.go
//
// 基于 Redis 状态机的多轮对话会话管理。
//
// 设计思路：
//   - 每个 (userId, sessionId) 唯一对应一个会话状态（SessionState）
//   - 状态机流转：Idle → InConversation → Idle（支持手动重置）
//   - 对话历史以 JSON 序列化存入 Redis，带 TTL 自动过期（防止内存泄漏）
//   - 群聊场景下多个用户共享同一 sessionId，各用户的上下文相互独立
//   - 上下文感知窗口（ContextWindow）：群聊时截取组内近 N 条消息作为背景注入
//   - 支持会话隔离：私聊/群聊 key 命名空间隔离
//
// Redis key 规范：
//   - ai:session:{userId}:{sessionId}:state   → 状态字符串
//   - ai:session:{userId}:{sessionId}:history → JSON 历史消息列表
//   - ai:context:{sessionId}:window          → 群聊上下文窗口（近 N 条消息）
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	myredis "kama_chat_server/internal/service/redis"
	"kama_chat_server/pkg/zlog"
)

// ─────────────────────────────────────────────────────────
// 会话状态枚举
// ─────────────────────────────────────────────────────────

// SessionState 会话状态机（AI 对话流程控制）
type SessionState string

const (
	// SessionStateIdle 空闲：用户未触发 AI 对话
	SessionStateIdle SessionState = "idle"

	// SessionStateInConversation 对话中：用户已通过 @ai 触发，AI 正在处理或等待继续
	SessionStateInConversation SessionState = "in_conversation"

	// SessionStateWaiting 等待 AI 推理：任务已提交协程池，等待结果回写
	SessionStateWaiting SessionState = "waiting"
)

// ─────────────────────────────────────────────────────────
// Redis Key 前缀
// ─────────────────────────────────────────────────────────

const (
	redisKeyStatePrefix   = "ai:session:%s:%s:state"   // (userId, sessionId)
	redisKeyHistoryPrefix = "ai:session:%s:%s:history" // (userId, sessionId)
	redisKeyWindowPrefix  = "ai:context:%s:window"     // (sessionId)

	// sessionTTL 会话在 Redis 中的存活时间（30 分钟无操作自动过期）
	sessionTTL = 30 * time.Minute

	// contextWindowSize 群聊上下文窗口大小（注入到系统 Prompt 的近 N 条消息）
	contextWindowSize = 10
)

// ─────────────────────────────────────────────────────────
// 数据结构
// ─────────────────────────────────────────────────────────

// SessionMessage 存储在 Redis 历史中的单条对话消息
type SessionMessage struct {
	Role      string    `json:"role"`      // "user" | "assistant" | "system"
	Content   string    `json:"content"`   // 消息正文
	Timestamp time.Time `json:"timestamp"` // 消息时间戳
}

// ContextWindowEntry 群聊上下文窗口中的一条聊天记录（用于 AI 感知群聊背景）
type ContextWindowEntry struct {
	SenderID   string    `json:"sender_id"`   // 发送者 UUID
	SenderName string    `json:"sender_name"` // 发送者昵称
	Content    string    `json:"content"`     // 消息内容
	Timestamp  time.Time `json:"timestamp"`
}

// ─────────────────────────────────────────────────────────
// RedisSessionManager
// ─────────────────────────────────────────────────────────

// RedisSessionManager 管理所有用户的 AI 对话会话状态（Redis 后端）。
type RedisSessionManager struct {
	mu           sync.Mutex
	contextLimit int // 单用户历史消息保留条数（1 条 = 1 条 role+content）
}

var (
	sessionManagerOnce     sync.Once
	sessionManagerInstance *RedisSessionManager
)

// GetSessionManager 返回 RedisSessionManager 全局单例。
func GetSessionManager() *RedisSessionManager {
	sessionManagerOnce.Do(func() {
		sessionManagerInstance = &RedisSessionManager{
			contextLimit: 20, // 默认保留最近 20 条消息（约 10 轮对话）
		}
		zlog.Info("[session-manager] Redis session manager initialized")
	})
	return sessionManagerInstance
}

// ─────────────────────────────────────────────────────────
// 状态机操作
// ─────────────────────────────────────────────────────────

// GetState 获取当前会话状态，若不存在则返回 Idle。
func (m *RedisSessionManager) GetState(ctx context.Context, userID, sessionID string) SessionState {
	key := fmt.Sprintf(redisKeyStatePrefix, userID, sessionID)
	val, err := myredis.GetKey(key)
	if err != nil || val == "" {
		return SessionStateIdle
	}
	return SessionState(val)
}

// SetState 更新会话状态，并刷新 TTL。
func (m *RedisSessionManager) SetState(ctx context.Context, userID, sessionID string, state SessionState) error {
	key := fmt.Sprintf(redisKeyStatePrefix, userID, sessionID)
	return myredis.SetKeyEx(key, string(state), sessionTTL)
}

// TransitionToConversation 将会话状态从 Idle 切换到 InConversation。
// 幂等：已处于 InConversation 时直接返回 nil。
func (m *RedisSessionManager) TransitionToConversation(ctx context.Context, userID, sessionID string) error {
	state := m.GetState(ctx, userID, sessionID)
	if state == SessionStateInConversation {
		return nil
	}
	return m.SetState(ctx, userID, sessionID, SessionStateInConversation)
}

// ResetSession 将会话恢复为 Idle，并清除历史记录（用于用户主动重置或 TTL 过期后重启）。
func (m *RedisSessionManager) ResetSession(ctx context.Context, userID, sessionID string) error {
	stateKey := fmt.Sprintf(redisKeyStatePrefix, userID, sessionID)
	histKey := fmt.Sprintf(redisKeyHistoryPrefix, userID, sessionID)
	_ = myredis.DelKeyIfExists(stateKey)
	_ = myredis.DelKeyIfExists(histKey)
	zlog.Info(fmt.Sprintf("[session-manager] session reset: user=%s, session=%s", userID, sessionID))
	return nil
}

// ─────────────────────────────────────────────────────────
// 对话历史管理
// ─────────────────────────────────────────────────────────

// AppendHistory 向会话历史末尾追加一条消息，并维持最大条数限制。
func (m *RedisSessionManager) AppendHistory(ctx context.Context, userID, sessionID string, msg SessionMessage) error {
	history, err := m.GetHistory(ctx, userID, sessionID)
	if err != nil {
		history = make([]SessionMessage, 0)
	}

	history = append(history, msg)

	// 滑动窗口截断（防止 Redis value 无限增长）
	if len(history) > m.contextLimit {
		history = history[len(history)-m.contextLimit:]
	}

	return m.saveHistory(ctx, userID, sessionID, history)
}

// GetHistory 获取完整对话历史列表。若无历史记录返回空切片。
func (m *RedisSessionManager) GetHistory(ctx context.Context, userID, sessionID string) ([]SessionMessage, error) {
	key := fmt.Sprintf(redisKeyHistoryPrefix, userID, sessionID)
	val, err := myredis.GetKey(key)
	if err != nil || val == "" {
		return []SessionMessage{}, nil
	}

	var msgs []SessionMessage
	if err := json.Unmarshal([]byte(val), &msgs); err != nil {
		zlog.Error("[session-manager] history unmarshal error: " + err.Error())
		return []SessionMessage{}, nil
	}
	return msgs, nil
}

// GetHistoryAsMap 以 map[role][]content 形式返回历史（便于构建 LLM 请求体）。
func (m *RedisSessionManager) GetHistoryAsMap(ctx context.Context, userID, sessionID string) []map[string]string {
	history, _ := m.GetHistory(ctx, userID, sessionID)
	result := make([]map[string]string, 0, len(history))
	for _, msg := range history {
		result = append(result, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}
	return result
}

// saveHistory 将历史序列化后写入 Redis，刷新 TTL。
func (m *RedisSessionManager) saveHistory(ctx context.Context, userID, sessionID string, history []SessionMessage) error {
	data, err := json.Marshal(history)
	if err != nil {
		return err
	}
	key := fmt.Sprintf(redisKeyHistoryPrefix, userID, sessionID)
	return myredis.SetKeyEx(key, string(data), sessionTTL)
}

// ─────────────────────────────────────────────────────────
// 群聊上下文感知窗口
// ─────────────────────────────────────────────────────────

// PushGroupContext 向群聊上下文窗口推入一条聊天记录（不含 @ai 消息）。
// 窗口大小为 contextWindowSize，超出时丢弃最旧的。
func (m *RedisSessionManager) PushGroupContext(ctx context.Context, sessionID string, entry ContextWindowEntry) error {
	window, _ := m.GetGroupContext(ctx, sessionID)
	window = append(window, entry)
	if len(window) > contextWindowSize {
		window = window[len(window)-contextWindowSize:]
	}

	data, err := json.Marshal(window)
	if err != nil {
		return err
	}
	key := fmt.Sprintf(redisKeyWindowPrefix, sessionID)
	return myredis.SetKeyEx(key, string(data), sessionTTL)
}

// GetGroupContext 获取当前群聊上下文窗口（最近 contextWindowSize 条消息）。
func (m *RedisSessionManager) GetGroupContext(ctx context.Context, sessionID string) ([]ContextWindowEntry, error) {
	key := fmt.Sprintf(redisKeyWindowPrefix, sessionID)
	val, err := myredis.GetKey(key)
	if err != nil || val == "" {
		return []ContextWindowEntry{}, nil
	}

	var window []ContextWindowEntry
	if err := json.Unmarshal([]byte(val), &window); err != nil {
		return []ContextWindowEntry{}, nil
	}
	return window, nil
}

// BuildGroupContextPrompt 将群聊上下文窗口格式化为自然语言片段，
// 注入到 System Prompt 中，使 AI 能感知群聊背景。
//
// 示例输出：
//
//	【群聊背景（最近 10 条消息）】
//	- Alice: 今天天气怎么样？
//	- Bob: 好像要下雨
func (m *RedisSessionManager) BuildGroupContextPrompt(ctx context.Context, sessionID string) string {
	window, err := m.GetGroupContext(ctx, sessionID)
	if err != nil || len(window) == 0 {
		return ""
	}

	prompt := "【群聊背景（最近消息）】\n"
	for _, entry := range window {
		prompt += fmt.Sprintf("- %s: %s\n", entry.SenderName, entry.Content)
	}
	prompt += "---\n请基于以上群聊背景，回答下面用户的问题：\n"
	return prompt
}

// ─────────────────────────────────────────────────────────
// 辅助方法
// ─────────────────────────────────────────────────────────

// SessionExists 检查会话是否存在（有效期内）。
func (m *RedisSessionManager) SessionExists(ctx context.Context, userID, sessionID string) bool {
	state := m.GetState(ctx, userID, sessionID)
	return state != SessionStateIdle
}

// GetSessionInfo 返回会话摘要信息（用于 metrics / debug）。
func (m *RedisSessionManager) GetSessionInfo(ctx context.Context, userID, sessionID string) map[string]interface{} {
	state := m.GetState(ctx, userID, sessionID)
	history, _ := m.GetHistory(ctx, userID, sessionID)
	return map[string]interface{}{
		"user_id":       userID,
		"session_id":    sessionID,
		"state":         string(state),
		"history_count": len(history),
	}
}

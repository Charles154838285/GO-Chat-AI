// Package ai — eino_chain.go
// 基于字节跳动 Eino 框架实现 AI 问答链，替换直接 HTTP 调用方式。
//
// 架构说明：
//   - EinoChain 封装 Eino ChatModel + 多轮对话上下文管理
//   - 对外提供 Chat（同步）/ StreamChat（流式）两种调用接口
//   - 内部管理每个 userId 的对话历史（最多保留 ContextLimit 轮）
//   - 通过 Eino schema.Message 标准格式与 LLM 通信（OpenAI 兼容协议）
//
// 依赖：
//   - github.com/cloudwego/eino v0.3.x
//   - github.com/cloudwego/eino-ext v0.3.x  (Ark/Doubao provider)
package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"kama_chat_server/internal/config"
	"kama_chat_server/pkg/zlog"
)

// ──────────────────────────────────────────────
// 对话历史管理
// ──────────────────────────────────────────────

// turnHistory 存储单个用户的多轮对话历史
type turnHistory struct {
	turns     []*schema.Message // 按顺序存储 human/assistant 消息
	updatedAt time.Time
}

// ──────────────────────────────────────────────
// EinoChain：核心 AI 问答链
// ──────────────────────────────────────────────

// EinoChain 封装 Eino 框架的 ChatModel，提供带上下文管理的多轮对话能力。
// 通过协程池调用，保证主聊天流程不被阻塞。
type EinoChain struct {
	chatModel    model.ChatModel
	systemPrompt string
	contextLimit int // 每用户最多保留的历史轮数（一轮 = 1 human + 1 assistant）

	historyMu sync.RWMutex
	history   map[string]*turnHistory // userId -> 对话历史
}

var (
	einoChainInstance *EinoChain
	einoChainOnce     sync.Once
)

// GetEinoChain 返回全局单例。若初始化失败则返回 nil，调用方需检查。
func GetEinoChain() *EinoChain {
	einoChainOnce.Do(func() {
		cfg := &config.GetConfig().AIConfig
		if !cfg.Enabled {
			zlog.Info("[eino] AI chain disabled, skip initialization")
			return
		}
		chain, err := newEinoChain(cfg)
		if err != nil {
			zlog.Error("[eino] chain initialization failed: " + err.Error())
			return
		}
		einoChainInstance = chain
		zlog.Info(fmt.Sprintf("[eino] chain initialized: provider=%s, model=%s", cfg.Provider, cfg.Model))
	})
	return einoChainInstance
}

// newEinoChain 构造 EinoChain，使用 Ark (ByteDance/Doubao) provider。
func newEinoChain(cfg *config.AIConfig) (*EinoChain, error) {
	if cfg.ApiKey == "" {
		return nil, errors.New("API key is empty")
	}

	maxTokens := cfg.MaxTokens
	temp := float32(cfg.Temperature)

	// 初始化 Ark (豆包/火山方舟) ChatModel
	chatModel, err := ark.NewChatModel(context.Background(), &ark.ChatModelConfig{
		APIKey:      cfg.ApiKey,
		Model:       cfg.Model,
		BaseURL:     cfg.BaseURL,
		MaxTokens:   &maxTokens,
		Temperature: &temp,
	})
	if err != nil {
		return nil, fmt.Errorf("ark.NewChatModel: %w", err)
	}

	contextLimit := cfg.ContextLimit
	if contextLimit <= 0 {
		contextLimit = 10
	}

	return &EinoChain{
		chatModel:    chatModel,
		systemPrompt: cfg.SystemPrompt,
		contextLimit: contextLimit,
		history:      make(map[string]*turnHistory),
	}, nil
}

// ──────────────────────────────────────────────
// 公开接口
// ──────────────────────────────────────────────

// Chat 同步问答：将用户消息发送到 LLM，返回助手回复文本。
// 内部自动维护多轮上下文。
func (c *EinoChain) Chat(ctx context.Context, userID, userMsg string) (string, error) {
	msgs := c.buildMessages(userID, userMsg)

	resp, err := c.chatModel.Generate(ctx, msgs)
	if err != nil {
		return "", fmt.Errorf("chatModel.Generate: %w", err)
	}
	reply := resp.Content
	c.updateHistory(userID, userMsg, reply)
	return reply, nil
}

// StreamChat 流式问答：每产生一个 token 片段就调用 onChunk，全部完成后调用 onDone。
// 适用于前端实时打字效果展示。
func (c *EinoChain) StreamChat(
	ctx context.Context,
	userID, userMsg string,
	onChunk func(chunk string),
	onDone func(fullReply string, err error),
) {
	msgs := c.buildMessages(userID, userMsg)

	reader, err := c.chatModel.Stream(ctx, msgs)
	if err != nil {
		onDone("", fmt.Errorf("chatModel.Stream: %w", err))
		return
	}
	defer reader.Close()

	var fullReply string
	for {
		chunk, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			onDone(fullReply, err)
			return
		}
		if chunk != nil && chunk.Content != "" {
			onChunk(chunk.Content)
			fullReply += chunk.Content
		}
	}

	c.updateHistory(userID, userMsg, fullReply)
	onDone(fullReply, nil)
}

// ClearHistory 清除指定用户的对话历史。
func (c *EinoChain) ClearHistory(userID string) {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()
	delete(c.history, userID)
	zlog.Info(fmt.Sprintf("[eino] history cleared for user: %s", userID))
}

// HistoryInfo 返回指定用户当前保留的历史轮数。
func (c *EinoChain) HistoryInfo(userID string) int {
	c.historyMu.RLock()
	defer c.historyMu.RUnlock()
	h, ok := c.history[userID]
	if !ok {
		return 0
	}
	// 每轮包含 human + assistant 共 2 条消息
	return len(h.turns) / 2
}

// ──────────────────────────────────────────────
// 内部方法
// ──────────────────────────────────────────────

// buildMessages 组装完整请求消息列表：[system] + [历史] + [当前 human]
func (c *EinoChain) buildMessages(userID, userMsg string) []*schema.Message {
	msgs := make([]*schema.Message, 0, c.contextLimit*2+2)

	// 1. System prompt
	if c.systemPrompt != "" {
		msgs = append(msgs, schema.SystemMessage(c.systemPrompt))
	}

	// 2. 历史对话（截取最近 contextLimit 轮）
	c.historyMu.RLock()
	if h, ok := c.history[userID]; ok {
		start := 0
		maxTurns := c.contextLimit * 2
		if len(h.turns) > maxTurns {
			start = len(h.turns) - maxTurns
		}
		msgs = append(msgs, h.turns[start:]...)
	}
	c.historyMu.RUnlock()

	// 3. 当前用户消息
	msgs = append(msgs, schema.HumanMessage(userMsg))
	return msgs
}

// updateHistory 将本轮对话追加到历史记录中。
func (c *EinoChain) updateHistory(userID, userMsg, assistantReply string) {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()

	h, ok := c.history[userID]
	if !ok {
		h = &turnHistory{}
		c.history[userID] = h
	}
	h.turns = append(h.turns,
		schema.HumanMessage(userMsg),
		schema.AssistantMessage(assistantReply, nil),
	)
	h.updatedAt = time.Now()

	// 防止历史无限增长：超过 2*contextLimit 条时截断
	maxMessages := c.contextLimit * 2
	if len(h.turns) > maxMessages {
		h.turns = h.turns[len(h.turns)-maxMessages:]
	}
}

// IsAvailable 返回 EinoChain 是否可用（初始化成功）。
func (c *EinoChain) IsAvailable() bool {
	return c != nil && c.chatModel != nil
}

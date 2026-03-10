package handler

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
	"go.uber.org/zap"

	pb "kama_ai_service/proto/pb"
)

// Config 是 AIHandler 的初始化配置，由 main.go 传入。
type Config struct {
	APIKey       string
	Model        string
	BaseURL      string
	SystemPrompt string
	MaxTokens    int
	Temperature  float64
	ContextLimit int
}

// AIHandler 实现 pb.AIServiceServer，封装 Eino ChatModel 和多用户对话历史管理。
type AIHandler struct {
	chatModel    model.ChatModel
	systemPrompt string
	contextLimit int

	historyMu sync.RWMutex
	history   map[string][]*schema.Message // userId -> 对话历史
	logger    *zap.Logger
}

// NewAIHandler 创建并初始化 AIHandler，使用 Ark（火山方舟/豆包）作为底层 LLM provider。
func NewAIHandler(cfg *Config, logger *zap.Logger) (*AIHandler, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("ai-service: API key is required")
	}

	maxTokens := cfg.MaxTokens
	temp := float32(cfg.Temperature)

	// 初始化字节跳动火山方舟 ChatModel（Eino 框架）
	chatModel, err := ark.NewChatModel(context.Background(), &ark.ChatModelConfig{
		APIKey:      cfg.APIKey,
		Model:       cfg.Model,
		BaseURL:     cfg.BaseURL,
		MaxTokens:   &maxTokens,
		Temperature: &temp,
	})
	if err != nil {
		return nil, fmt.Errorf("ark.NewChatModel: %w", err)
	}

	limit := cfg.ContextLimit
	if limit <= 0 {
		limit = 10
	}

	return &AIHandler{
		chatModel:    chatModel,
		systemPrompt: cfg.SystemPrompt,
		contextLimit: limit,
		history:      make(map[string][]*schema.Message),
		logger:       logger,
	}, nil
}

// Chat 同步问答：将用户消息发送到 LLM，返回完整回复文本。
// gRPC Unary RPC 实现。
func (h *AIHandler) Chat(ctx context.Context, req *pb.ChatRequest) (*pb.ChatResponse, error) {
	if req.Content == "" {
		return &pb.ChatResponse{Code: 400, Message: "content is empty"}, nil
	}

	msgs := h.buildMessages(req.UserId, req.Content)
	h.logger.Info("calling LLM (sync)",
		zap.String("user_id", req.UserId),
		zap.Int("history_msgs", len(msgs)))

	resp, err := h.chatModel.Generate(ctx, msgs)
	if err != nil {
		h.logger.Error("LLM generate error", zap.Error(err))
		return &pb.ChatResponse{Code: 500, Message: err.Error()}, nil
	}

	reply := resp.Content
	h.updateHistory(req.UserId, req.Content, reply)

	h.logger.Info("LLM reply ok",
		zap.String("user_id", req.UserId),
		zap.Int("reply_len", len(reply)))
	return &pb.ChatResponse{Content: reply, Code: 0}, nil
}

// StreamChat 流式问答：通过 server-side streaming 逐 Token 推送，适合前端打字效果。
// gRPC Server-streaming RPC 实现。
func (h *AIHandler) StreamChat(req *pb.ChatRequest, stream pb.AIService_StreamChatServer) error {
	if req.Content == "" {
		return stream.Send(&pb.StreamChunk{Done: true, Code: 400})
	}

	msgs := h.buildMessages(req.UserId, req.Content)
	ctx := stream.Context()

	reader, err := h.chatModel.Stream(ctx, msgs)
	if err != nil {
		h.logger.Error("LLM stream error", zap.Error(err))
		return stream.Send(&pb.StreamChunk{Done: true, Code: 500})
	}
	defer reader.Close()

	var fullReply string
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			h.logger.Error("stream recv error", zap.Error(recvErr))
			return recvErr
		}
		if chunk != nil && chunk.Content != "" {
			fullReply += chunk.Content
			if sendErr := stream.Send(&pb.StreamChunk{Content: chunk.Content, Done: false}); sendErr != nil {
				return sendErr
			}
		}
	}

	// 流结束标志
	if err := stream.Send(&pb.StreamChunk{Done: true, Code: 0}); err != nil {
		return err
	}

	h.updateHistory(req.UserId, req.Content, fullReply)
	h.logger.Info("stream chat completed",
		zap.String("user_id", req.UserId),
		zap.Int("reply_len", len(fullReply)))
	return nil
}

// ClearContext 清除指定用户的对话历史（gRPC Unary）。
func (h *AIHandler) ClearContext(_ context.Context, req *pb.ClearContextRequest) (*pb.ClearContextResponse, error) {
	h.historyMu.Lock()
	delete(h.history, req.UserId)
	h.historyMu.Unlock()
	h.logger.Info("context cleared", zap.String("user_id", req.UserId))
	return &pb.ClearContextResponse{Success: true, Message: "context cleared"}, nil
}

// GetContextInfo 查询用户当前对话历史轮数（gRPC Unary）。
func (h *AIHandler) GetContextInfo(_ context.Context, req *pb.GetContextInfoRequest) (*pb.GetContextInfoResponse, error) {
	h.historyMu.RLock()
	turns := len(h.history[req.UserId]) / 2 // 每轮 = 1 human + 1 assistant
	h.historyMu.RUnlock()
	return &pb.GetContextInfoResponse{
		UserId:    req.UserId,
		TurnCount: int32(turns),
		Limit:     int32(h.contextLimit),
	}, nil
}

// HealthCheck 服务健康探测（gRPC Unary）。
func (h *AIHandler) HealthCheck(_ context.Context, _ *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	return &pb.HealthCheckResponse{Status: "ok", Version: "1.0.0"}, nil
}

// buildMessages 组装完整请求消息列表：[system] + [历史（截取最近 contextLimit 轮）] + [当前用户消息]
func (h *AIHandler) buildMessages(userID, userMsg string) []*schema.Message {
	msgs := make([]*schema.Message, 0, h.contextLimit*2+2)

	if h.systemPrompt != "" {
		msgs = append(msgs, schema.SystemMessage(h.systemPrompt))
	}

	h.historyMu.RLock()
	hist := h.history[userID]
	maxMessages := h.contextLimit * 2
	if len(hist) > maxMessages {
		hist = hist[len(hist)-maxMessages:]
	}
	msgs = append(msgs, hist...)
	h.historyMu.RUnlock()

	msgs = append(msgs, schema.HumanMessage(userMsg))
	return msgs
}

// updateHistory 追加本轮对话并截断超出部分，防止内存无限增长。
func (h *AIHandler) updateHistory(userID, userMsg, assistantReply string) {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()

	h.history[userID] = append(h.history[userID],
		schema.HumanMessage(userMsg),
		schema.AssistantMessage(assistantReply, nil),
	)

	maxMessages := h.contextLimit * 2
	if len(h.history[userID]) > maxMessages {
		h.history[userID] = h.history[userID][len(h.history[userID])-maxMessages:]
	}
	_ = time.Now() // 扩展点：可记录最后活跃时间用于 LRU 清理
}

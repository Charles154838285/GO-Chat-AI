package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"kama_chat_server/internal/dao"
	"kama_chat_server/internal/dto/request"
	"kama_chat_server/internal/dto/respond"
	"kama_chat_server/internal/model"
	"kama_chat_server/internal/service/sequence"
	"kama_chat_server/pkg/enum/message/message_status_enum"
	"kama_chat_server/pkg/enum/message/message_type_enum"
	"kama_chat_server/pkg/util/random"
	"kama_chat_server/pkg/zlog"
)

// MessageHandler AI消息处理器
type MessageHandler struct {
	aiService    *AIService
	orchestrator *AgentOrchestrator
}

var messageHandler *MessageHandler

func init() {
	messageHandler = &MessageHandler{
		aiService: GetAIService(),
	}
	messageHandler.orchestrator = NewAgentOrchestrator(messageHandler.aiService)
}

// GetMessageHandler 获取消息处理器实例
func GetMessageHandler() *MessageHandler {
	return messageHandler
}

// ShouldHandleByAI 判断消息是否应该由AI处理
func (h *MessageHandler) ShouldHandleByAI(content string) bool {
	if !h.aiService.IsEnabled() {
		return false
	}

	// 检查是否包含 @ai 触发词
	lowerContent := strings.ToLower(strings.TrimSpace(content))
	return strings.HasPrefix(lowerContent, "@ai") || strings.Contains(lowerContent, "@ai ")
}

// ExtractAIMessage 提取发给AI的实际消息内容
func (h *MessageHandler) ExtractAIMessage(content string) string {
	// 移除 @ai 前缀
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "@ai")
	content = strings.TrimPrefix(content, "@AI")
	content = strings.TrimPrefix(content, "@Ai")
	return strings.TrimSpace(content)
}

// HandleAIMessage 处理AI消息（同步模式）
func (h *MessageHandler) HandleAIMessage(ctx context.Context, req *request.ChatMessageRequest) (*respond.GetMessageListRespond, error) {
	if !h.aiService.IsEnabled() {
		return nil, fmt.Errorf("AI服务未启用")
	}

	// 提取实际消息内容
	userMessage := h.ExtractAIMessage(req.Content)
	if userMessage == "" {
		userMessage = "你好"
	}

	// 调用角色化 Agent 编排链路（语义缓存 + Function Calling + RAG + LLM）
	aiResponse := ""
	if h.orchestrator != nil {
		reply, meta, err := h.orchestrator.GenerateReply(ctx, req, userMessage)
		if err != nil {
			zlog.Error("Agent编排失败: " + err.Error())
		} else {
			aiResponse = reply
			zlog.Info(fmt.Sprintf("Agent处理完成 role=%s cache_hit=%v tool=%s rag=%v latency_ms=%d",
				meta.Role, meta.CacheHit, meta.ToolUsed, meta.RAGUsed, meta.LatencyMs))
		}
	}
	if aiResponse == "" {
		fallback, ret := h.aiService.Chat(ctx, req.SendId, userMessage)
		if ret != 0 {
			zlog.Error(fmt.Sprintf("AI调用失败, ret=%d, response=%s", ret, fallback))
			fallback = "抱歉，我现在有点忙，请稍后再试～"
		}
		aiResponse = fallback
	}

	// 获取AI用户信息
	aiUser, err := GetAIUserInfo()
	if err != nil {
		zlog.Error("获取AI用户信息失败: " + err.Error())
		return nil, err
	}

	// 存储AI回复消息到数据库
	aiMessage := model.Message{
		Uuid:       fmt.Sprintf("M%s", random.GetNowAndLenRandomString(11)),
		SessionId:  req.SessionId,
		Type:       message_type_enum.Text,
		Content:    aiResponse,
		Url:        "",
		SendId:     AI_USER_UUID,
		SendName:   aiUser.Nickname,
		SendAvatar: aiUser.Avatar,
		ReceiveId:  req.SendId, // AI回复给原发送者
		FileSize:   "0B",
		FileType:   "",
		FileName:   "",
		Status:     message_status_enum.Sent, // AI消息直接标记为已发送
		CreatedAt:  time.Now(),
		AVdata:     "",
	}

	if err := dao.GormDB.Create(&aiMessage).Error; err != nil {
		zlog.Error("AI消息存储失败: " + err.Error())
		return nil, err
	}

	// 构建响应
	serverSeq := req.ServerSeq
	if serverSeq <= 0 {
		serverSeq = sequence.GlobalSequencer.Next(req.SessionId)
	}
	traceID := req.TraceId
	if traceID == "" {
		traceID = sequence.GlobalSequencer.TraceID(req.SessionId, req.SendId, serverSeq)
	}
	messageRsp := &respond.GetMessageListRespond{
		SendId:     aiMessage.SendId,
		SendName:   aiMessage.SendName,
		SendAvatar: aiMessage.SendAvatar,
		ReceiveId:  aiMessage.ReceiveId,
		ClientSeq:  req.ClientSeq,
		ServerSeq:  serverSeq,
		TraceId:    traceID,
		Type:       aiMessage.Type,
		Content:    aiMessage.Content,
		Url:        aiMessage.Url,
		FileSize:   aiMessage.FileSize,
		FileName:   aiMessage.FileName,
		FileType:   aiMessage.FileType,
		CreatedAt:  aiMessage.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	zlog.Info(fmt.Sprintf("AI回复成功: user=%s, response=%s", req.SendId, aiResponse))
	return messageRsp, nil
}

// HandleAIMessageStream 处理AI消息（流式模式）
// 返回值: (完整AI回复, messageUUID, error)
func (h *MessageHandler) HandleAIMessageStream(ctx context.Context, req *request.ChatMessageRequest, streamCallback func(chunk string) error) (string, string, error) {
	if !h.aiService.IsEnabled() {
		return "", "", fmt.Errorf("AI服务未启用")
	}

	userMessage := h.ExtractAIMessage(req.Content)
	if userMessage == "" {
		userMessage = "你好"
	}

	var fullResponse strings.Builder

	// 流式回调包装
	wrappedCallback := func(chunk string) error {
		fullResponse.WriteString(chunk)
		return streamCallback(chunk)
	}

	// 调用流式AI服务
	ret, err := h.aiService.StreamChat(ctx, req.SendId, userMessage, wrappedCallback)
	if err != nil || ret != 0 {
		zlog.Error(fmt.Sprintf("AI流式调用失败: %v, ret=%d", err, ret))
		return "", "", err
	}

	aiResponse := fullResponse.String()

	// 获取AI用户信息
	aiUser, err := GetAIUserInfo()
	if err != nil {
		zlog.Error("获取AI用户信息失败: " + err.Error())
		return "", "", err
	}

	// 存储完整的AI回复到数据库
	messageUUID := fmt.Sprintf("M%s", random.GetNowAndLenRandomString(11))
	aiMessage := model.Message{
		Uuid:       messageUUID,
		SessionId:  req.SessionId,
		Type:       message_type_enum.Text,
		Content:    aiResponse,
		Url:        "",
		SendId:     AI_USER_UUID,
		SendName:   aiUser.Nickname,
		SendAvatar: aiUser.Avatar,
		ReceiveId:  req.SendId,
		FileSize:   "0B",
		FileType:   "",
		FileName:   "",
		Status:     message_status_enum.Sent,
		CreatedAt:  time.Now(),
		AVdata:     "",
	}

	if err := dao.GormDB.Create(&aiMessage).Error; err != nil {
		zlog.Error("AI消息存储失败: " + err.Error())
		return "", "", err
	}

	zlog.Info(fmt.Sprintf("AI流式回复完成: user=%s, length=%d", req.SendId, len(aiResponse)))
	return aiResponse, messageUUID, nil
}

// BuildAIResponseMessage 构建AI响应消息结构
func (h *MessageHandler) BuildAIResponseMessage(aiResponse, sessionId, receiveId string) (*respond.GetMessageListRespond, error) {
	aiUser, err := GetAIUserInfo()
	if err != nil {
		return nil, err
	}

	serverSeq := sequence.GlobalSequencer.Next(sessionId)
	return &respond.GetMessageListRespond{
		SendId:     AI_USER_UUID,
		SendName:   aiUser.Nickname,
		SendAvatar: aiUser.Avatar,
		ReceiveId:  receiveId,
		ServerSeq:  serverSeq,
		TraceId:    sequence.GlobalSequencer.TraceID(sessionId, AI_USER_UUID, serverSeq),
		Type:       message_type_enum.Text,
		Content:    aiResponse,
		Url:        "",
		FileSize:   "0B",
		FileName:   "",
		FileType:   "",
		CreatedAt:  time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}

// StreamResponse 将响应通过WebSocket流式发送
type StreamResponse struct {
	Type      string `json:"type"`       // "ai_chunk" or "ai_complete"
	Content   string `json:"content"`    // chunk内容或完整消息
	MessageID string `json:"message_id"` // 消息UUID（仅complete时有值）
	Timestamp string `json:"timestamp"`
}

// CreateStreamChunk 创建流式chunk响应
func CreateStreamChunk(chunk string) ([]byte, error) {
	resp := StreamResponse{
		Type:      "ai_chunk",
		Content:   chunk,
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	}
	return json.Marshal(resp)
}

// CreateStreamComplete 创建流式完成响应
func CreateStreamComplete(fullMessage, messageID string) ([]byte, error) {
	resp := StreamResponse{
		Type:      "ai_complete",
		Content:   fullMessage,
		MessageID: messageID,
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	}
	return json.Marshal(resp)
}

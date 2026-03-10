package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kama_chat_server/internal/dto/request"
	"kama_chat_server/pkg/zlog"
)

type AgentRole string

const (
	RoleGeneral AgentRole = "general_assistant"
	RoleTech    AgentRole = "tech_expert"
	RoleOps     AgentRole = "ops_support"
	RoleTutor   AgentRole = "learning_tutor"
)

// AgentMeta captures orchestration decisions for observability and debugging.
type AgentMeta struct {
	Role         AgentRole `json:"role"`
	CacheHit     bool      `json:"cache_hit"`
	ToolUsed     string    `json:"tool_used,omitempty"`
	RAGUsed      bool      `json:"rag_used"`
	LatencyMs    int64     `json:"latency_ms"`
	SessionState string    `json:"session_state"`
}

// AgentOrchestrator coordinates semantic cache, function calling, RAG, session state and LLM.
type AgentOrchestrator struct {
	aiService      *AIService
	semanticCache  *SemanticCache
	sessionManager *RedisSessionManager
	ragService     *RAGService
	tools          *ToolRegistry
}

func NewAgentOrchestrator(aiService *AIService) *AgentOrchestrator {
	return &AgentOrchestrator{
		aiService:      aiService,
		semanticCache:  GetSemanticCache(MockEmbeddingFunc),
		sessionManager: GetSessionManager(),
		ragService:     GetRAGService(),
		tools:          GetToolRegistry(),
	}
}

func (o *AgentOrchestrator) GenerateReply(ctx context.Context, req *request.ChatMessageRequest, userMessage string) (string, AgentMeta, error) {
	start := time.Now()
	meta := AgentMeta{}

	meta.Role = inferRole(userMessage)
	_ = o.sessionManager.TransitionToConversation(ctx, req.SendId, req.SessionId)
	_ = o.sessionManager.AppendHistory(ctx, req.SendId, req.SessionId, SessionMessage{
		Role:      "user",
		Content:   userMessage,
		Timestamp: time.Now(),
	})

	if cached, hit, _ := o.semanticCache.Get(ctx, userMessage); hit {
		meta.CacheHit = true
		meta.SessionState = string(o.sessionManager.GetState(ctx, req.SendId, req.SessionId))
		meta.LatencyMs = time.Since(start).Milliseconds()
		return cached, meta, nil
	}

	if tool, toolArgs := detectToolCall(userMessage); tool != "" {
		result, err := o.tools.Dispatch(ctx, tool, toolArgs)
		if err == nil {
			meta.ToolUsed = tool
			meta.SessionState = string(o.sessionManager.GetState(ctx, req.SendId, req.SessionId))
			meta.LatencyMs = time.Since(start).Milliseconds()
			_ = o.semanticCache.Set(ctx, userMessage, result)
			_ = o.sessionManager.AppendHistory(ctx, req.SendId, req.SessionId, SessionMessage{Role: "assistant", Content: result, Timestamp: time.Now()})
			return result, meta, nil
		}
		zlog.Error("[agent] tool call failed: " + err.Error())
	}

	enrichedMessage := userMessage
	if req.ReceiveId != "" && strings.HasPrefix(req.ReceiveId, "G") {
		if !strings.Contains(strings.ToLower(userMessage), "@ai") {
			_ = o.sessionManager.PushGroupContext(ctx, req.SessionId, ContextWindowEntry{
				SenderID:   req.SendId,
				SenderName: req.SendName,
				Content:    userMessage,
				Timestamp:  time.Now(),
			})
		}
		ctxPrompt := o.sessionManager.BuildGroupContextPrompt(ctx, req.SessionId)
		if ctxPrompt != "" {
			enrichedMessage = ctxPrompt + "\n用户问题：" + userMessage
		}
	}

	if shouldUseRAG(userMessage) {
		results, err := o.ragService.Search(ctx, userMessage, 3)
		if err == nil && len(results) > 0 {
			meta.RAGUsed = true
			enrichedMessage = o.ragService.BuildRAGPrompt(results) + "\n用户问题：" + userMessage
		}
	}

	reply, ret := o.aiService.Chat(ctx, req.SendId, buildRolePrompt(meta.Role)+"\n"+enrichedMessage)
	if ret != 0 {
		reply = "抱歉，我现在有点忙，请稍后再试。"
	}

	_ = o.semanticCache.Set(ctx, userMessage, reply)
	_ = o.sessionManager.AppendHistory(ctx, req.SendId, req.SessionId, SessionMessage{
		Role:      "assistant",
		Content:   reply,
		Timestamp: time.Now(),
	})
	meta.SessionState = string(o.sessionManager.GetState(ctx, req.SendId, req.SessionId))
	meta.LatencyMs = time.Since(start).Milliseconds()
	return reply, meta, nil
}

func inferRole(content string) AgentRole {
	lc := strings.ToLower(content)
	switch {
	case strings.Contains(lc, "代码") || strings.Contains(lc, "go") || strings.Contains(lc, "bug"):
		return RoleTech
	case strings.Contains(lc, "部署") || strings.Contains(lc, "kafka") || strings.Contains(lc, "redis") || strings.Contains(lc, "监控"):
		return RoleOps
	case strings.Contains(lc, "解释") || strings.Contains(lc, "学习") || strings.Contains(lc, "原理"):
		return RoleTutor
	default:
		return RoleGeneral
	}
}

func buildRolePrompt(role AgentRole) string {
	switch role {
	case RoleTech:
		return "你是技术专家 Agent，请给出准确、可执行的技术建议，优先提供步骤和边界条件。"
	case RoleOps:
		return "你是运维专家 Agent，请从稳定性、可观测性、容错和回滚角度回答。"
	case RoleTutor:
		return "你是教学型 Agent，请分步骤解释并给出简短示例。"
	default:
		return "你是群聊智能助手 Agent，请简洁、友好并保持上下文一致。"
	}
}

func shouldUseRAG(content string) bool {
	lc := strings.ToLower(content)
	return strings.Contains(lc, "文档") || strings.Contains(lc, "知识库") || strings.Contains(lc, "规范") || strings.Contains(lc, "手册")
}

func detectToolCall(content string) (toolName, args string) {
	lc := strings.ToLower(strings.TrimSpace(content))
	switch {
	case strings.Contains(lc, "现在几点") || strings.Contains(lc, "当前时间"):
		return "get_current_time", "{}"
	case strings.Contains(lc, "天气"):
		city := "北京"
		if idx := strings.Index(content, "天气"); idx > 0 {
			prefix := strings.TrimSpace(content[:idx])
			if prefix != "" {
				city = prefix
			}
		}
		return "get_weather", fmt.Sprintf(`{"city":"%s"}`, city)
	case strings.Contains(lc, "计算"):
		expr := strings.TrimSpace(strings.ReplaceAll(content, "计算", ""))
		if expr == "" {
			expr = "1+1"
		}
		return "calculator", fmt.Sprintf(`{"expression":"%s"}`, strings.ReplaceAll(expr, "\"", ""))
	case strings.Contains(lc, "查知识库") || strings.Contains(lc, "检索"):
		query := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(content, "查知识库", ""), "检索", ""))
		if query == "" {
			query = content
		}
		return "search_knowledge_base", fmt.Sprintf(`{"query":"%s","top_k":3}`, strings.ReplaceAll(query, "\"", ""))
	default:
		return "", ""
	}
}

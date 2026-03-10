package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"kama_chat_server/internal/config"
	"kama_chat_server/pkg/zlog"
)

// AIService AI服务结构体
type AIService struct {
	config        *config.AIConfig
	contextCache  map[string]*ConversationContext // userId -> context
	cacheMutex    sync.RWMutex
	isInitialized bool
}

// ConversationContext 对话上下文
type ConversationContext struct {
	Messages  []ChatMessage
	UpdatedAt time.Time
}

// ChatMessage 聊天消息结构
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMRequest 大模型请求
type LLMRequest struct {
	Messages    []ChatMessage `json:"messages"`
	Model       string        `json:"model"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream,omitempty"`
}

// LLMResponse 大模型响应
type LLMResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

var (
	aiServiceInstance *AIService
	once              sync.Once
)

// GetAIService 获取AI服务单例
func GetAIService() *AIService {
	once.Do(func() {
		aiServiceInstance = &AIService{
			contextCache: make(map[string]*ConversationContext),
		}
		if err := aiServiceInstance.Initialize(); err != nil {
			zlog.Error("AI服务初始化失败: " + err.Error())
		}
	})
	return aiServiceInstance
}

// Initialize 初始化AI服务
func (s *AIService) Initialize() error {
	s.config = &config.GetConfig().AIConfig

	if !s.config.Enabled {
		zlog.Info("AI服务未启用")
		return nil
	}

	if s.config.ApiKey == "" || s.config.ApiKey == "your-api-key-here" {
		zlog.Error("AI服务配置错误: 未设置API Key")
		return errors.New("未设置API Key")
	}

	if s.config.Model == "" {
		zlog.Error("AI服务配置错误: 未设置模型")
		return errors.New("未设置模型")
	}

	s.isInitialized = true
	zlog.Info(fmt.Sprintf("AI服务初始化成功, 提供商: %s, 模型: %s", s.config.Provider, s.config.Model))
	return nil
}

// IsEnabled 检查AI服务是否启用
func (s *AIService) IsEnabled() bool {
	return s.config != nil && s.config.Enabled && s.isInitialized
}

// Chat 同步对话（非流式）
func (s *AIService) Chat(ctx context.Context, userID, userMessage string) (string, int) {
	if !s.IsEnabled() {
		return "AI服务未启用", -2
	}

	// 构建消息历史
	messages := s.buildMessages(userID, userMessage)

	// 调用LLM API
	aiResponse, err := s.callLLMAPI(ctx, messages)
	if err != nil {
		zlog.Error("AI调用失败: " + err.Error())
		return "AI暂时无法回复，请稍后再试", -1
	}

	if aiResponse == "" {
		zlog.Error("AI返回空响应")
		return "AI暂时无法回复，请稍后再试", -2
	}

	// 更新上下文
	s.updateContext(userID, userMessage, aiResponse)

	return aiResponse, 0
}

// StreamChat 流式对话
func (s *AIService) StreamChat(ctx context.Context, userID, userMessage string, callback func(string) error) (int, error) {
	if !s.IsEnabled() {
		return -2, errors.New("AI服务未启用")
	}

	messages := s.buildMessages(userID, userMessage)

	// 调用流式API
	fullResponse := ""
	err := s.callLLMStreamAPI(ctx, messages, func(chunk string) error {
		fullResponse += chunk
		return callback(chunk)
	})

	if err != nil {
		zlog.Error("AI流式调用失败: " + err.Error())
		return -1, err
	}

	// 更新上下文
	if fullResponse != "" {
		s.updateContext(userID, userMessage, fullResponse)
	}

	return 0, nil
}

// buildMessages 构建消息列表（包含上下文）
func (s *AIService) buildMessages(userID, userMessage string) []ChatMessage {
	messages := []ChatMessage{
		{
			Role:    "system",
			Content: s.config.SystemPrompt,
		},
	}

	// 添加历史上下文
	s.cacheMutex.RLock()
	if ctx, exists := s.contextCache[userID]; exists {
		messages = append(messages, ctx.Messages...)
	}
	s.cacheMutex.RUnlock()

	// 添加当前用户消息
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: userMessage,
	})

	return messages
}

// updateContext 更新对话上下文
func (s *AIService) updateContext(userID, userMsg, aiMsg string) {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	ctx, exists := s.contextCache[userID]
	if !exists {
		ctx = &ConversationContext{
			Messages: make([]ChatMessage, 0),
		}
		s.contextCache[userID] = ctx
	}

	// 添加用户消息
	ctx.Messages = append(ctx.Messages, ChatMessage{
		Role:    "user",
		Content: userMsg,
	})

	// 添加AI回复
	ctx.Messages = append(ctx.Messages, ChatMessage{
		Role:    "assistant",
		Content: aiMsg,
	})

	// 限制上下文长度（保留最近N轮对话，一轮=用户+AI两条消息）
	maxMessages := s.config.ContextLimit * 2
	if len(ctx.Messages) > maxMessages {
		ctx.Messages = ctx.Messages[len(ctx.Messages)-maxMessages:]
	}

	ctx.UpdatedAt = time.Now()
}

// ClearContext 清除用户上下文
func (s *AIService) ClearContext(userID string) {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()
	delete(s.contextCache, userID)
	zlog.Info(fmt.Sprintf("已清除用户 %s 的AI对话上下文", userID))
}

// GetContextInfo 获取上下文信息（用于调试）
func (s *AIService) GetContextInfo(userID string) (int, time.Time) {
	s.cacheMutex.RLock()
	defer s.cacheMutex.RUnlock()

	if ctx, exists := s.contextCache[userID]; exists {
		return len(ctx.Messages) / 2, ctx.UpdatedAt
	}
	return 0, time.Time{}
}

// callLLMAPI 调用LLM API（同步）
func (s *AIService) callLLMAPI(ctx context.Context, messages []ChatMessage) (string, error) {
	req := LLMRequest{
		Messages:    messages,
		Model:       s.config.Model,
		MaxTokens:   s.config.MaxTokens,
		Temperature: s.config.Temperature,
		Stream:      false,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", s.config.BaseURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.config.ApiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if len(body) == 0 {
		return "", fmt.Errorf("LLM响应为空，status=%d", resp.StatusCode)
	}

	var llmResp LLMResponse
	if err := json.Unmarshal(body, &llmResp); err != nil {
		return "", fmt.Errorf("解析LLM响应失败: %w, body=%s", err, string(body))
	}

	if len(llmResp.Choices) == 0 || llmResp.Choices[0].Message.Content == "" {
		if llmResp.Error.Message != "" {
			return "", errors.New(llmResp.Error.Message)
		}
		return "", errors.New("无效的LLM响应")
	}

	return llmResp.Choices[0].Message.Content, nil
}

// callLLMStreamAPI 调用LLM流式API
func (s *AIService) callLLMStreamAPI(ctx context.Context, messages []ChatMessage, callback func(string) error) error {
	req := LLMRequest{
		Messages:    messages,
		Model:       s.config.Model,
		MaxTokens:   s.config.MaxTokens,
		Temperature: s.config.Temperature,
		Stream:      true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", s.config.BaseURL, bytes.NewReader(data))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.config.ApiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			if choices, ok := event["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if content, ok := delta["content"].(string); ok && content != "" {
							if err := callback(content); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}

	return scanner.Err()
}

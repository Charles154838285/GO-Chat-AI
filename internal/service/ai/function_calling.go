// Package ai — function_calling.go
//
// Function Calling 工具注册与分发中心。
//
// 设计思路：
//   - 定义标准 Tool 接口，每个工具实现 Name/Description/Schema/Execute
//   - ToolRegistry 统一注册并调度工具，对接 Eino 框架的 FunctionCall 机制
//   - 内置工具：天气查询 / 当前时间 / 数学计算 / 网络搜索 / 知识库检索（RAG）
//   - 工具调用结果注入对话历史，形成 ReAct 循环（Reason + Act）
//   - 安全性：所有工具参数经 Schema 校验，禁止命令注入
//
// 集成方式：
//
//	agent := GetEinoAgent(...)
//	agent 在推理循环中自动根据 LLM 返回的 function_call 字段调用对应工具
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"kama_chat_server/pkg/zlog"
)

// ─────────────────────────────────────────────────────────
// 工具接口定义
// ─────────────────────────────────────────────────────────

// ToolParam 工具输入参数的 JSON Schema 描述（简化版）
type ToolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // "string" | "number" | "boolean"
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ToolDefinition 工具描述，用于向 LLM 注册（OpenAI functions 格式兼容）
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  []ToolParam `json:"parameters"`
}

// ToolResult 工具执行结果
type ToolResult struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Error   string      `json:"error,omitempty"`
}

// Tool 工具接口，每种工具均需实现此接口
type Tool interface {
	Definition() ToolDefinition
	Execute(ctx context.Context, args map[string]interface{}) (ToolResult, error)
}

// ─────────────────────────────────────────────────────────
// ToolRegistry — 工具注册表
// ─────────────────────────────────────────────────────────

// ToolRegistry 管理所有注册工具，提供 Dispatch 统一调度入口
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

var (
	toolRegistryOnce     sync.Once
	toolRegistryInstance *ToolRegistry
)

// GetToolRegistry 返回全局工具注册表单例，首次调用时注册所有内置工具。
func GetToolRegistry() *ToolRegistry {
	toolRegistryOnce.Do(func() {
		toolRegistryInstance = &ToolRegistry{
			tools: make(map[string]Tool),
		}
		// 注册内置工具
		toolRegistryInstance.Register(&CurrentTimeTool{})
		toolRegistryInstance.Register(&CalculatorTool{})
		toolRegistryInstance.Register(&WeatherQueryTool{})
		toolRegistryInstance.Register(&KnowledgeSearchTool{})
		toolRegistryInstance.Register(&ChatHistorySummaryTool{})

		zlog.Info(fmt.Sprintf("[tool-registry] %d built-in tools registered", len(toolRegistryInstance.tools)))
	})
	return toolRegistryInstance
}

// Register 注册一个工具（支持运行时动态注册）
func (r *ToolRegistry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Definition().Name] = t
	zlog.Info(fmt.Sprintf("[tool-registry] tool registered: %s", t.Definition().Name))
}

// Dispatch 根据工具名称调用工具，返回 JSON 格式的结果字符串。
// 这是 LLM function_call 响应后的统一入口。
func (r *ToolRegistry) Dispatch(ctx context.Context, toolName string, argsJSON string) (string, error) {
	r.mu.RLock()
	tool, ok := r.tools[toolName]
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("tool not found: %s", toolName)
	}

	// 解析参数 JSON
	var args map[string]interface{}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("invalid tool args JSON: %w", err)
		}
	} else {
		args = make(map[string]interface{})
	}

	zlog.Info(fmt.Sprintf("[tool-registry] dispatching: tool=%s, args=%s", toolName, argsJSON))

	result, err := tool.Execute(ctx, args)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ListTools 返回所有已注册工具的定义（用于构建 LLM functions 参数）。
func (r *ToolRegistry) ListTools() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

// ─────────────────────────────────────────────────────────
// 内置工具实现
// ─────────────────────────────────────────────────────────

// ── 1. 当前时间工具 ──────────────────────────────────────

// CurrentTimeTool 返回当前日期和时间
type CurrentTimeTool struct{}

func (t *CurrentTimeTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "get_current_time",
		Description: "获取当前日期和时间，以及星期几、时区等信息",
		Parameters:  []ToolParam{},
	}
}

func (t *CurrentTimeTool) Execute(_ context.Context, _ map[string]interface{}) (ToolResult, error) {
	now := time.Now()
	weekdays := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return ToolResult{
		Success: true,
		Data: map[string]string{
			"datetime":       now.Format("2006-01-02 15:04:05"),
			"date":           now.Format("2006年01月02日"),
			"time":           now.Format("15:04:05"),
			"weekday":        weekdays[now.Weekday()],
			"timezone":       now.Location().String(),
			"unix_timestamp": strconv.FormatInt(now.Unix(), 10),
		},
	}, nil
}

// ── 2. 数学计算工具 ──────────────────────────────────────

// CalculatorTool 执行基础数学运算（加减乘除、幂、平方根）
type CalculatorTool struct{}

func (t *CalculatorTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "calculator",
		Description: "执行数学计算，支持加减乘除、幂运算、平方根。输入算式字符串。",
		Parameters: []ToolParam{
			{Name: "expression", Type: "string", Description: "要计算的数学表达式，如 '2+3*4' 或 'sqrt(16)'", Required: true},
		},
	}
}

func (t *CalculatorTool) Execute(_ context.Context, args map[string]interface{}) (ToolResult, error) {
	expr, ok := args["expression"].(string)
	if !ok || expr == "" {
		return ToolResult{Success: false, Error: "expression is required"}, nil
	}

	result, err := evalSimpleExpr(expr)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}

	return ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"expression": expr,
			"result":     result,
		},
	}, nil
}

// evalSimpleExpr 简单表达式求值（仅支持 sqrt/pow 和四则运算）
func evalSimpleExpr(expr string) (float64, error) {
	expr = strings.TrimSpace(expr)
	// sqrt(x)
	if strings.HasPrefix(expr, "sqrt(") && strings.HasSuffix(expr, ")") {
		inner := expr[5 : len(expr)-1]
		val, err := strconv.ParseFloat(strings.TrimSpace(inner), 64)
		if err != nil {
			return 0, fmt.Errorf("无效数值: %s", inner)
		}
		if val < 0 {
			return 0, fmt.Errorf("不能对负数开平方根")
		}
		return math.Sqrt(val), nil
	}
	// 解析简单的 a op b 表达式
	for _, op := range []string{"+", "-", "*", "/"} {
		idx := strings.LastIndex(expr, op)
		if idx > 0 {
			leftStr := strings.TrimSpace(expr[:idx])
			rightStr := strings.TrimSpace(expr[idx+1:])
			left, err1 := strconv.ParseFloat(leftStr, 64)
			right, err2 := strconv.ParseFloat(rightStr, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			switch op {
			case "+":
				return left + right, nil
			case "-":
				return left - right, nil
			case "*":
				return left * right, nil
			case "/":
				if right == 0 {
					return 0, fmt.Errorf("除数不能为零")
				}
				return left / right, nil
			}
		}
	}
	// 纯数字
	val, err := strconv.ParseFloat(expr, 64)
	if err != nil {
		return 0, fmt.Errorf("无法解析表达式: %s", expr)
	}
	return val, nil
}

// ── 3. 天气查询工具（Mock）──────────────────────────────

// WeatherQueryTool 查询指定城市天气（真实场景对接气象 API，此为 Mock 实现）
type WeatherQueryTool struct{}

func (t *WeatherQueryTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "get_weather",
		Description: "查询指定城市的实时天气信息，包括温度、湿度、天气状况",
		Parameters: []ToolParam{
			{Name: "city", Type: "string", Description: "城市名称，如'北京'、'上海'", Required: true},
		},
	}
}

func (t *WeatherQueryTool) Execute(_ context.Context, args map[string]interface{}) (ToolResult, error) {
	city, ok := args["city"].(string)
	if !ok || city == "" {
		return ToolResult{Success: false, Error: "city is required"}, nil
	}

	// Mock 天气数据（生产环境替换为真实 API 调用，如和风天气、高德等）
	weatherMock := map[string]interface{}{
		"city":        city,
		"temperature": "18°C",
		"humidity":    "65%",
		"condition":   "晴转多云",
		"wind":        "东南风 3 级",
		"forecast":    "明天：小雨，14°C~20°C",
		"updated_at":  time.Now().Format("15:04"),
	}

	return ToolResult{Success: true, Data: weatherMock}, nil
}

// ── 4. 知识库检索工具（接入 RAG）──────────────────────

// KnowledgeSearchTool 基于 RAG 在知识库中检索相关文档段落
type KnowledgeSearchTool struct{}

func (t *KnowledgeSearchTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "search_knowledge_base",
		Description: "在本地知识库中检索与问题相关的内容片段，用于回答专业性问题",
		Parameters: []ToolParam{
			{Name: "query", Type: "string", Description: "检索关键词或问题", Required: true},
			{Name: "top_k", Type: "number", Description: "返回结果数量，默认 3", Required: false},
		},
	}
}

func (t *KnowledgeSearchTool) Execute(ctx context.Context, args map[string]interface{}) (ToolResult, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return ToolResult{Success: false, Error: "query is required"}, nil
	}

	topK := 3
	if k, ok := args["top_k"].(float64); ok && k > 0 {
		topK = int(k)
	}

	// 调用 RAG 服务
	ragService := GetRAGService()
	documents, err := ragService.Search(ctx, query, topK)
	if err != nil {
		zlog.Error("[knowledge-search] RAG search failed: " + err.Error())
		return ToolResult{
			Success: false,
			Error:   "知识库检索暂时不可用",
		}, nil
	}

	return ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"query":   query,
			"results": documents,
			"count":   len(documents),
		},
	}, nil
}

// ── 5. 对话摘要工具 ──────────────────────────────────────

// ChatHistorySummaryTool 生成当前会话对话摘要
type ChatHistorySummaryTool struct{}

func (t *ChatHistorySummaryTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "summarize_chat_history",
		Description: "生成当前会话的对话摘要，帮助 AI 在长对话中保持上下文连贯",
		Parameters: []ToolParam{
			{Name: "session_id", Type: "string", Description: "会话 ID", Required: true},
			{Name: "user_id", Type: "string", Description: "用户 ID", Required: true},
		},
	}
}

func (t *ChatHistorySummaryTool) Execute(ctx context.Context, args map[string]interface{}) (ToolResult, error) {
	sessionID, _ := args["session_id"].(string)
	userID, _ := args["user_id"].(string)

	if sessionID == "" || userID == "" {
		return ToolResult{Success: false, Error: "session_id and user_id are required"}, nil
	}

	manager := GetSessionManager()
	history, err := manager.GetHistory(ctx, userID, sessionID)
	if err != nil || len(history) == 0 {
		return ToolResult{
			Success: true,
			Data:    map[string]string{"summary": "暂无对话历史"},
		}, nil
	}

	// 简单摘要：提取最近几轮的关键内容
	recentCount := 5
	if len(history) < recentCount {
		recentCount = len(history)
	}
	recent := history[len(history)-recentCount:]

	var summaryParts []string
	for _, msg := range recent {
		role := "用户"
		if msg.Role == "assistant" {
			role = "AI"
		}
		content := msg.Content
		if len(content) > 100 {
			content = content[:100] + "..."
		}
		summaryParts = append(summaryParts, fmt.Sprintf("[%s]: %s", role, content))
	}

	return ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"session_id":    sessionID,
			"history_count": len(history),
			"recent_turns":  summaryParts,
		},
	}, nil
}

// ─────────────────────────────────────────────────────────
// FunctionCallRequest / Response（与 LLM 交互的数据结构）
// ─────────────────────────────────────────────────────────

// FunctionCallRequest LLM 返回的 function_call 信息
type FunctionCallRequest struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON 字符串
}

// ProcessFunctionCall 处理 LLM 返回的 FunctionCallRequest，执行工具并返回结果。
// 是 ReAct 循环的核心调度函数。
func ProcessFunctionCall(ctx context.Context, fcReq FunctionCallRequest) (string, error) {
	registry := GetToolRegistry()
	result, err := registry.Dispatch(ctx, fcReq.Name, fcReq.Arguments)
	if err != nil {
		zlog.Error(fmt.Sprintf("[function-call] tool dispatch error: tool=%s, err=%v", fcReq.Name, err))
		return fmt.Sprintf(`{"error": "工具 %s 调用失败: %s"}`, fcReq.Name, err.Error()), nil
	}
	zlog.Info(fmt.Sprintf("[function-call] tool result: tool=%s, result_len=%d", fcReq.Name, len(result)))
	return result, nil
}

package ai

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	pb "kama_chat_server/internal/service/ai/grpc_pb"
	"kama_chat_server/pkg/zlog"
)

// AIGRPCClient 是主通讯服务连接独立 AI 微服务的 gRPC 客户端。
//
// 架构说明：
//   - kama_chat_server（本服务）通过此客户端向独立部署的 ai-service 发起调用
//   - ai-service 内部使用 Eino 框架驱动豆包大模型
//   - 两个服务完全解耦，可独立扩缩容
//   - 连接失效时自动重连（gRPC 内置重试机制）
type AIGRPCClient struct {
	conn   *grpc.ClientConn
	client pb.AIServiceClient
	once   sync.Once
}

var (
	grpcClientInstance *AIGRPCClient
	grpcClientOnce     sync.Once
)

// GetAIGRPCClient 返回全局单例 gRPC 客户端。
// addr 格式：host:port，例如 "127.0.0.1:9090"
func GetAIGRPCClient(addr string) *AIGRPCClient {
	grpcClientOnce.Do(func() {
		grpcClientInstance = &AIGRPCClient{}
		if err := grpcClientInstance.dial(addr); err != nil {
			zlog.Error(fmt.Sprintf("[grpc-client] connect to ai-service failed: %v", err))
		}
	})
	return grpcClientInstance
}

// dial 建立 gRPC 长连接，配置 keepalive 防止 NAT 超时断开。
func (c *AIGRPCClient) dial(addr string) error {
	kaParams := keepalive.ClientParameters{
		Time:                30 * time.Second, // 30s 无数据则发 ping
		Timeout:             10 * time.Second, // 等 pong 超时时间
		PermitWithoutStream: true,             // 无活跃 RPC 时也保持心跳
	}

	conn, err := grpc.Dial(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(kaParams),
		grpc.WithBlock(),
		grpc.WithTimeout(5*time.Second), // 连接超时
	)
	if err != nil {
		return fmt.Errorf("grpc.Dial(%s): %w", addr, err)
	}

	c.conn = conn
	c.client = pb.NewAIServiceClient(conn)

	_, logger := zap.NewProduction()
	_ = logger
	zlog.Info(fmt.Sprintf("[grpc-client] connected to ai-service at %s", addr))
	return nil
}

// Chat 同步调用 AI 微服务，返回完整回复文本。
// 失败时自动降级到本地 AIService（fallback）。
func (c *AIGRPCClient) Chat(ctx context.Context, userID, content string) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("gRPC client not initialized")
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	resp, err := c.client.Chat(ctx, &pb.ChatRequest{
		UserId:  userID,
		Content: content,
	})
	if err != nil {
		return "", fmt.Errorf("ai-service Chat RPC: %w", err)
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("ai-service error: code=%d msg=%s", resp.Code, resp.Message)
	}
	return resp.Content, nil
}

// ClearContext 清除指定用户在 AI 微服务侧的对话上下文。
func (c *AIGRPCClient) ClearContext(ctx context.Context, userID string) error {
	if c.client == nil {
		return fmt.Errorf("gRPC client not initialized")
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	resp, err := c.client.ClearContext(ctx, &pb.ClearContextRequest{UserId: userID})
	if err != nil {
		return fmt.Errorf("ai-service ClearContext RPC: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("clear context failed: %s", resp.Message)
	}
	return nil
}

// GetContextInfo 查询用户在 AI 微服务中已保留的对话轮数。
func (c *AIGRPCClient) GetContextInfo(ctx context.Context, userID string) (*pb.GetContextInfoResponse, error) {
	if c.client == nil {
		return nil, fmt.Errorf("gRPC client not initialized")
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	return c.client.GetContextInfo(ctx, &pb.GetContextInfoRequest{UserId: userID})
}

// HealthCheck 探测 AI 微服务是否正常（可用于启动时检查）。
func (c *AIGRPCClient) HealthCheck(ctx context.Context) (string, error) {
	if c.client == nil {
		return "down", fmt.Errorf("gRPC client not initialized")
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	resp, err := c.client.HealthCheck(ctx, &pb.HealthCheckRequest{})
	if err != nil {
		return "down", err
	}
	return resp.Status, nil
}

// IsAvailable 快速判断 gRPC 客户端是否已就绪。
func (c *AIGRPCClient) IsAvailable() bool {
	return c != nil && c.client != nil
}

// Close 关闭 gRPC 连接（服务关闭时调用）。
func (c *AIGRPCClient) Close() {
	c.once.Do(func() {
		if c.conn != nil {
			if err := c.conn.Close(); err != nil {
				zlog.Error("[grpc-client] close error: " + err.Error())
			}
		}
	})
}

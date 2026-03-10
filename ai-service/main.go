package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"kama_ai_service/internal/handler"
	pb "kama_ai_service/proto/pb"
)

// ---- 配置结构 ----------------------------------------------------------------

type Config struct {
	Server ServerConfig `toml:"server"`
	AI     AIConfig     `toml:"ai"`
}

type ServerConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

type AIConfig struct {
	APIKey       string  `toml:"apiKey"`
	Model        string  `toml:"model"`
	BaseURL      string  `toml:"baseURL"`
	SystemPrompt string  `toml:"systemPrompt"`
	MaxTokens    int     `toml:"maxTokens"`
	Temperature  float64 `toml:"temperature"`
	ContextLimit int     `toml:"contextLimit"`
}

func loadConfig() *Config {
	cfg := &Config{}
	for _, p := range []string{"./configs/ai_service.toml", "../configs/ai_service.toml"} {
		if _, err := toml.DecodeFile(p, cfg); err == nil {
			return cfg
		}
	}
	// 从环境变量兜底，方便 Docker/K8s 部署
	cfg.Server.Host = envOr("AI_SERVICE_HOST", "0.0.0.0")
	cfg.Server.Port = 9090
	cfg.AI.APIKey = envOr("AI_API_KEY", "")
	cfg.AI.Model = envOr("AI_MODEL", "doubao-seed-1-8-251228")
	cfg.AI.BaseURL = envOr("AI_BASE_URL", "https://ark.cn-beijing.volces.com/api/v3")
	cfg.AI.SystemPrompt = "你是KamaChat的智能助手，请简洁、准确地回答用户问题。"
	cfg.AI.MaxTokens = 1000
	cfg.AI.Temperature = 0.7
	cfg.AI.ContextLimit = 10
	return cfg
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---- gRPC 拦截器 -------------------------------------------------------------

// unaryLogger 记录每次 Unary RPC 的耗时和错误。
func unaryLogger(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		h grpc.UnaryHandler,
	) (interface{}, error) {
		start := time.Now()
		resp, err := h(ctx, req)
		if err != nil {
			logger.Error("gRPC unary error",
				zap.String("method", info.FullMethod),
				zap.Duration("elapsed", time.Since(start)),
				zap.Error(err))
		} else {
			logger.Info("gRPC unary ok",
				zap.String("method", info.FullMethod),
				zap.Duration("elapsed", time.Since(start)))
		}
		return resp, err
	}
}

// streamLogger 记录 Server-streaming RPC 耗时。
func streamLogger(logger *zap.Logger) grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		h grpc.StreamHandler,
	) error {
		start := time.Now()
		err := h(srv, ss)
		logger.Info("gRPC stream done",
			zap.String("method", info.FullMethod),
			zap.Duration("elapsed", time.Since(start)),
			zap.Error(err))
		return err
	}
}

// ---- main -------------------------------------------------------------------

func main() {
	cfg := loadConfig()

	logger, _ := zap.NewProduction()
	defer logger.Sync() //nolint:errcheck

	// 初始化 AI Handler（Eino 框架 + 字节跳动火山方舟/豆包大模型）
	aiHandler, err := handler.NewAIHandler(&handler.Config{
		APIKey:       cfg.AI.APIKey,
		Model:        cfg.AI.Model,
		BaseURL:      cfg.AI.BaseURL,
		SystemPrompt: cfg.AI.SystemPrompt,
		MaxTokens:    cfg.AI.MaxTokens,
		Temperature:  cfg.AI.Temperature,
		ContextLimit: cfg.AI.ContextLimit,
	}, logger)
	if err != nil {
		log.Fatalf("init AI handler: %v", err)
	}
	logger.Info("AI handler initialized",
		zap.String("model", cfg.AI.Model),
		zap.String("provider", "ByteDance Ark/Doubao"))

	// 绑定 TCP 端口
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}

	// 创建 gRPC Server + 日志拦截器
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(unaryLogger(logger)),
		grpc.StreamInterceptor(streamLogger(logger)),
	)

	// 注册 AIService 实现
	pb.RegisterAIServiceServer(grpcServer, aiHandler)

	// 开启 gRPC 反射（支持 grpcurl 调试）
	reflection.Register(grpcServer)

	logger.Info("AI gRPC microservice started", zap.String("addr", addr))

	// 异步启动服务，主 goroutine 等待退出信号
	go func() {
		if serveErr := grpcServer.Serve(lis); serveErr != nil {
			logger.Fatal("gRPC Serve error", zap.Error(serveErr))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down AI microservice...")
	grpcServer.GracefulStop()
	logger.Info("AI microservice stopped")
}

// Package grpcpb 包含 AI 微服务 gRPC 接口的消息类型和客户端存根。
//
// 与 ai-service/proto/pb 保持完全一致；实际项目中可通过 go workspace 共享。
// 若安装了 protoc，请在项目根执行：
//
//	protoc --go_out=. --go_opt=paths=source_relative \
//	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
//	       proto/ai_service.proto
//
// 并将生成产物替换本文件。
package grpcpb

import (
	"context"

	"google.golang.org/grpc"
)

// ---- Proto 消息（等价 protoc 生成）------------------------------------------

// ChatRequest AI 聊天请求
type ChatRequest struct {
	UserId    string `protobuf:"bytes,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	SessionId string `protobuf:"bytes,2,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	Content   string `protobuf:"bytes,3,opt,name=content,proto3" json:"content,omitempty"`
}

// ChatResponse AI 聊天响应
type ChatResponse struct {
	Content string `protobuf:"bytes,1,opt,name=content,proto3" json:"content,omitempty"`
	Code    int32  `protobuf:"varint,2,opt,name=code,proto3" json:"code,omitempty"`
	Message string `protobuf:"bytes,3,opt,name=message,proto3" json:"message,omitempty"`
}

// StreamChunk 流式响应分片
type StreamChunk struct {
	Content string `protobuf:"bytes,1,opt,name=content,proto3" json:"content,omitempty"`
	Done    bool   `protobuf:"varint,2,opt,name=done,proto3" json:"done,omitempty"`
	Code    int32  `protobuf:"varint,3,opt,name=code,proto3" json:"code,omitempty"`
}

// ClearContextRequest 清空上下文请求
type ClearContextRequest struct {
	UserId string `protobuf:"bytes,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
}

// ClearContextResponse 清空上下文响应
type ClearContextResponse struct {
	Success bool   `protobuf:"varint,1,opt,name=success,proto3" json:"success,omitempty"`
	Message string `protobuf:"bytes,2,opt,name=message,proto3" json:"message,omitempty"`
}

// GetContextInfoRequest 获取上下文信息请求
type GetContextInfoRequest struct {
	UserId string `protobuf:"bytes,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
}

// GetContextInfoResponse 获取上下文信息响应
type GetContextInfoResponse struct {
	UserId    string `protobuf:"bytes,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	TurnCount int32  `protobuf:"varint,2,opt,name=turn_count,json=turnCount,proto3" json:"turn_count,omitempty"`
	Limit     int32  `protobuf:"varint,3,opt,name=limit,proto3" json:"limit,omitempty"`
}

// HealthCheckRequest 健康检查请求
type HealthCheckRequest struct{}

// HealthCheckResponse 健康检查响应
type HealthCheckResponse struct {
	Status  string `protobuf:"bytes,1,opt,name=status,proto3" json:"status,omitempty"`
	Version string `protobuf:"bytes,2,opt,name=version,proto3" json:"version,omitempty"`
}

// ---- gRPC 服务接口 -----------------------------------------------------------

// AIServiceClient 是客户端调用接口
type AIServiceClient interface {
	Chat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (*ChatResponse, error)
	StreamChat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (AIService_StreamChatClient, error)
	ClearContext(ctx context.Context, in *ClearContextRequest, opts ...grpc.CallOption) (*ClearContextResponse, error)
	GetContextInfo(ctx context.Context, in *GetContextInfoRequest, opts ...grpc.CallOption) (*GetContextInfoResponse, error)
	HealthCheck(ctx context.Context, in *HealthCheckRequest, opts ...grpc.CallOption) (*HealthCheckResponse, error)
}

// AIService_StreamChatClient 是流式 RPC 的客户端读取接口
type AIService_StreamChatClient interface {
	grpc.ClientStream
	Recv() (*StreamChunk, error)
}

// AIServiceServer 是服务端需实现的接口（ai-service 内部使用）
type AIServiceServer interface {
	Chat(context.Context, *ChatRequest) (*ChatResponse, error)
	StreamChat(*ChatRequest, AIService_StreamChatServer) error
	ClearContext(context.Context, *ClearContextRequest) (*ClearContextResponse, error)
	GetContextInfo(context.Context, *GetContextInfoRequest) (*GetContextInfoResponse, error)
	HealthCheck(context.Context, *HealthCheckRequest) (*HealthCheckResponse, error)
}

// AIService_StreamChatServer 是流式 RPC 的服务端写入接口（ai-service 内部使用）
type AIService_StreamChatServer interface {
	grpc.ServerStream
	Send(*StreamChunk) error
}

// ---- 客户端存根实现 ----------------------------------------------------------

// aiServiceClient 客户端存根实现
type aiServiceClient struct {
	cc grpc.ClientConnInterface
}

// NewAIServiceClient 创建 gRPC 客户端存根
func NewAIServiceClient(cc grpc.ClientConnInterface) AIServiceClient {
	return &aiServiceClient{cc: cc}
}

// Chat 同步聊天 RPC 调用
func (c *aiServiceClient) Chat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (*ChatResponse, error) {
	out := new(ChatResponse)
	err := c.cc.Invoke(ctx, "/ai.AIService/Chat", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// StreamChat 流式聊天 RPC 调用
func (c *aiServiceClient) StreamChat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (AIService_StreamChatClient, error) {
	stream, err := c.cc.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/ai.AIService/StreamChat", opts...)
	if err != nil {
		return nil, err
	}

	x := &aiServiceStreamChatClient{ClientStream: stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

// ClearContext 清空上下文 RPC 调用
func (c *aiServiceClient) ClearContext(ctx context.Context, in *ClearContextRequest, opts ...grpc.CallOption) (*ClearContextResponse, error) {
	out := new(ClearContextResponse)
	err := c.cc.Invoke(ctx, "/ai.AIService/ClearContext", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetContextInfo 获取上下文信息 RPC 调用
func (c *aiServiceClient) GetContextInfo(ctx context.Context, in *GetContextInfoRequest, opts ...grpc.CallOption) (*GetContextInfoResponse, error) {
	out := new(GetContextInfoResponse)
	err := c.cc.Invoke(ctx, "/ai.AIService/GetContextInfo", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// HealthCheck 健康检查 RPC 调用
func (c *aiServiceClient) HealthCheck(ctx context.Context, in *HealthCheckRequest, opts ...grpc.CallOption) (*HealthCheckResponse, error) {
	out := new(HealthCheckResponse)
	err := c.cc.Invoke(ctx, "/ai.AIService/HealthCheck", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// aiServiceStreamChatClient 流式客户端实现
type aiServiceStreamChatClient struct {
	grpc.ClientStream
}

// Recv 读取流式响应
func (x *aiServiceStreamChatClient) Recv() (*StreamChunk, error) {
	m := new(StreamChunk)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---- 服务端注册（ai-service 内部使用）----------------------------------------

// RegisterAIServiceServer 将实现注册到 gRPC Server
func RegisterAIServiceServer(s grpc.ServiceRegistrar, srv AIServiceServer) {
	s.RegisterService(&AIService_ServiceDesc, srv)
}

// AIService_ServiceDesc gRPC 服务描述符
var AIService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "ai.AIService",
	HandlerType: (*AIServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Chat",
			Handler:    aiServiceChatHandler,
		},
		{
			MethodName: "ClearContext",
			Handler:    aiServiceClearContextHandler,
		},
		{
			MethodName: "GetContextInfo",
			Handler:    aiServiceGetContextInfoHandler,
		},
		{
			MethodName: "HealthCheck",
			Handler:    aiServiceHealthCheckHandler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "StreamChat",
			Handler:       aiServiceStreamChatHandler,
			ServerStreams: true,
		},
	},
}

// aiServiceChatHandler Chat 方法处理器
func aiServiceChatHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ChatRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AIServiceServer).Chat(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/ai.AIService/Chat"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AIServiceServer).Chat(ctx, req.(*ChatRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// aiServiceClearContextHandler ClearContext 方法处理器
func aiServiceClearContextHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ClearContextRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AIServiceServer).ClearContext(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/ai.AIService/ClearContext"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AIServiceServer).ClearContext(ctx, req.(*ClearContextRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// aiServiceGetContextInfoHandler GetContextInfo 方法处理器
func aiServiceGetContextInfoHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetContextInfoRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AIServiceServer).GetContextInfo(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/ai.AIService/GetContextInfo"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AIServiceServer).GetContextInfo(ctx, req.(*GetContextInfoRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// aiServiceHealthCheckHandler HealthCheck 方法处理器
func aiServiceHealthCheckHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(HealthCheckRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AIServiceServer).HealthCheck(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/ai.AIService/HealthCheck"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AIServiceServer).HealthCheck(ctx, req.(*HealthCheckRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// aiServiceStreamChatHandler StreamChat 流式方法处理器
func aiServiceStreamChatHandler(srv interface{}, stream grpc.ServerStream) error {
	m := new(ChatRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(AIServiceServer).StreamChat(m, &aiServiceStreamChatServer{stream})
}

// aiServiceStreamChatServer 流式服务端实现
type aiServiceStreamChatServer struct {
	grpc.ServerStream
}

// Send 发送流式响应
func (x *aiServiceStreamChatServer) Send(m *StreamChunk) error {
	return x.ServerStream.SendMsg(m)
}

// Package pb 包含 AI gRPC 微服务的消息类型、客户端存根及服务端注册逻辑。
//
// 实际项目请执行以下命令重新从 proto 生成：
//
//	cd <project_root>
//	protoc --go_out=ai-service --go_opt=paths=source_relative \
//	       --go-grpc_out=ai-service --go-grpc_opt=paths=source_relative \
//	       proto/ai_service.proto
package pb

import (
	"context"

	"google.golang.org/grpc"
)

// ---- Proto 消息 -------------------------------------------------------------

type ChatRequest struct {
	UserId    string `protobuf:"bytes,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	SessionId string `protobuf:"bytes,2,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	Content   string `protobuf:"bytes,3,opt,name=content,proto3" json:"content,omitempty"`
}

type ChatResponse struct {
	Content string `protobuf:"bytes,1,opt,name=content,proto3" json:"content,omitempty"`
	Code    int32  `protobuf:"varint,2,opt,name=code,proto3" json:"code,omitempty"`
	Message string `protobuf:"bytes,3,opt,name=message,proto3" json:"message,omitempty"`
}

type StreamChunk struct {
	Content string `protobuf:"bytes,1,opt,name=content,proto3" json:"content,omitempty"`
	Done    bool   `protobuf:"varint,2,opt,name=done,proto3" json:"done,omitempty"`
	Code    int32  `protobuf:"varint,3,opt,name=code,proto3" json:"code,omitempty"`
}

type ClearContextRequest struct {
	UserId string `protobuf:"bytes,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
}

type ClearContextResponse struct {
	Success bool   `protobuf:"varint,1,opt,name=success,proto3" json:"success,omitempty"`
	Message string `protobuf:"bytes,2,opt,name=message,proto3" json:"message,omitempty"`
}

type GetContextInfoRequest struct {
	UserId string `protobuf:"bytes,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
}

type GetContextInfoResponse struct {
	UserId    string `protobuf:"bytes,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	TurnCount int32  `protobuf:"varint,2,opt,name=turn_count,json=turnCount,proto3" json:"turn_count,omitempty"`
	Limit     int32  `protobuf:"varint,3,opt,name=limit,proto3" json:"limit,omitempty"`
}

type HealthCheckRequest struct{}

type HealthCheckResponse struct {
	Status  string `protobuf:"bytes,1,opt,name=status,proto3" json:"status,omitempty"`
	Version string `protobuf:"bytes,2,opt,name=version,proto3" json:"version,omitempty"`
}

// ---- Server 接口 ------------------------------------------------------------

type AIServiceServer interface {
	Chat(context.Context, *ChatRequest) (*ChatResponse, error)
	StreamChat(*ChatRequest, AIService_StreamChatServer) error
	ClearContext(context.Context, *ClearContextRequest) (*ClearContextResponse, error)
	GetContextInfo(context.Context, *GetContextInfoRequest) (*GetContextInfoResponse, error)
	HealthCheck(context.Context, *HealthCheckRequest) (*HealthCheckResponse, error)
}

type AIService_StreamChatServer interface {
	Send(*StreamChunk) error
	grpc.ServerStream
}

// ---- Client 接口 ------------------------------------------------------------

type AIServiceClient interface {
	Chat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (*ChatResponse, error)
	StreamChat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (AIService_StreamChatClient, error)
	ClearContext(ctx context.Context, in *ClearContextRequest, opts ...grpc.CallOption) (*ClearContextResponse, error)
	GetContextInfo(ctx context.Context, in *GetContextInfoRequest, opts ...grpc.CallOption) (*GetContextInfoResponse, error)
	HealthCheck(ctx context.Context, in *HealthCheckRequest, opts ...grpc.CallOption) (*HealthCheckResponse, error)
}

type AIService_StreamChatClient interface {
	Recv() (*StreamChunk, error)
	grpc.ClientStream
}

// ---- 客户端存根 --------------------------------------------------------------

type aiServiceClient struct{ cc grpc.ClientConnInterface }

func NewAIServiceClient(cc grpc.ClientConnInterface) AIServiceClient {
	return &aiServiceClient{cc}
}

func (c *aiServiceClient) Chat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (*ChatResponse, error) {
	out := new(ChatResponse)
	return out, c.cc.Invoke(ctx, "/ai.AIService/Chat", in, out, opts...)
}

func (c *aiServiceClient) StreamChat(ctx context.Context, in *ChatRequest, opts ...grpc.CallOption) (AIService_StreamChatClient, error) {
	stream, err := c.cc.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/ai.AIService/StreamChat", opts...)
	if err != nil {
		return nil, err
	}
	x := &aiSvcStreamChatClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type aiSvcStreamChatClient struct{ grpc.ClientStream }

func (x *aiSvcStreamChatClient) Recv() (*StreamChunk, error) {
	m := new(StreamChunk)
	return m, x.ClientStream.RecvMsg(m)
}

func (c *aiServiceClient) ClearContext(ctx context.Context, in *ClearContextRequest, opts ...grpc.CallOption) (*ClearContextResponse, error) {
	out := new(ClearContextResponse)
	return out, c.cc.Invoke(ctx, "/ai.AIService/ClearContext", in, out, opts...)
}

func (c *aiServiceClient) GetContextInfo(ctx context.Context, in *GetContextInfoRequest, opts ...grpc.CallOption) (*GetContextInfoResponse, error) {
	out := new(GetContextInfoResponse)
	return out, c.cc.Invoke(ctx, "/ai.AIService/GetContextInfo", in, out, opts...)
}

func (c *aiServiceClient) HealthCheck(ctx context.Context, in *HealthCheckRequest, opts ...grpc.CallOption) (*HealthCheckResponse, error) {
	out := new(HealthCheckResponse)
	return out, c.cc.Invoke(ctx, "/ai.AIService/HealthCheck", in, out, opts...)
}

// ---- 服务端注册 --------------------------------------------------------------

func RegisterAIServiceServer(s grpc.ServiceRegistrar, srv AIServiceServer) {
	s.RegisterService(&AIService_ServiceDesc, srv)
}

var AIService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "ai.AIService",
	HandlerType: (*AIServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Chat", Handler: _Chat_Handler},
		{MethodName: "ClearContext", Handler: _ClearContext_Handler},
		{MethodName: "GetContextInfo", Handler: _GetContextInfo_Handler},
		{MethodName: "HealthCheck", Handler: _HealthCheck_Handler},
	},
	Streams: []grpc.StreamDesc{
		{StreamName: "StreamChat", Handler: _StreamChat_Handler, ServerStreams: true},
	},
}

func _Chat_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ChatRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AIServiceServer).Chat(ctx, in)
	}
	return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/ai.AIService/Chat"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			return srv.(AIServiceServer).Chat(ctx, req.(*ChatRequest))
		})
}

func _ClearContext_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ClearContextRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AIServiceServer).ClearContext(ctx, in)
	}
	return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/ai.AIService/ClearContext"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			return srv.(AIServiceServer).ClearContext(ctx, req.(*ClearContextRequest))
		})
}

func _GetContextInfo_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetContextInfoRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AIServiceServer).GetContextInfo(ctx, in)
	}
	return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/ai.AIService/GetContextInfo"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			return srv.(AIServiceServer).GetContextInfo(ctx, req.(*GetContextInfoRequest))
		})
}

func _HealthCheck_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(HealthCheckRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AIServiceServer).HealthCheck(ctx, in)
	}
	return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: "/ai.AIService/HealthCheck"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			return srv.(AIServiceServer).HealthCheck(ctx, req.(*HealthCheckRequest))
		})
}

func _StreamChat_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(ChatRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(AIServiceServer).StreamChat(m, &aiSvcStreamChatServer{stream})
}

type aiSvcStreamChatServer struct{ grpc.ServerStream }

func (x *aiSvcStreamChatServer) Send(m *StreamChunk) error {
	return x.ServerStream.SendMsg(m)
}

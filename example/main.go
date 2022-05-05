package main

import (
	"context"
	_ "embed"
	proto "example/api"
	rkboot "github.com/rookie-ninja/rk-boot/v2"
	rkgrpc "github.com/rookie-ninja/rk-grpc/v2/boot"
	"google.golang.org/grpc"
	_ "rkgrpcweb/boot" //启动GRPC代理
)

//go:embed boot.yaml
var BootConfig []byte

func main() {
	ctx := context.Background()
	boot := rkboot.NewBoot(rkboot.WithBootConfigRaw(BootConfig))
	//启动GRPC服务器
	grpcEntry := rkgrpc.GetGrpcEntry("test")
	grpcEntry.AddRegFuncGrpc(registerGreeter)

	boot.Bootstrap(ctx)
	boot.WaitForShutdownSig(ctx)
}

func registerGreeter(server *grpc.Server) {
	proto.RegisterGreeterServer(server, &GreeterServer{})
}

type GreeterServer struct{}

func (g GreeterServer) SayHello(ctx context.Context, request *proto.HelloRequest) (*proto.HelloResponse, error) {
	return &proto.HelloResponse{
		Message: "Hello!",
	}, nil
}

package rkgrpcweb

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_logrus "github.com/grpc-ecosystem/go-grpc-middleware/logging/logrus"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/mwitkow/go-conntrack"
	"github.com/mwitkow/grpc-proxy/proxy"
	rkentry "github.com/rookie-ninja/rk-entry/v2/entry"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"log"
	"net"
	"net/http"
	"time"
)

func (e GRPCWebEntry) dialBackendOrFail() *grpc.ClientConn {
	var opt []grpc.DialOption
	opt = append(opt, grpc.WithCodec(proxy.Codec()))
	var gCredentials credentials.TransportCredentials
	if e.CertEntry != nil {
		gCredentials = credentials.NewTLS(
			&tls.Config{
				InsecureSkipVerify: true,
				Certificates: []tls.Certificate{
					*e.CertEntry.Certificate,
				},
			},
		)
	} else {
		gCredentials = insecure.NewCredentials()
	}
	opt = append(opt, grpc.WithTransportCredentials(gCredentials))
	if e.MaxCallRecvMsgSize > 0 {
		opt = append(opt,
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(e.MaxCallRecvMsgSize)),
		)
	} else {
		opt = append(opt,
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1024*1024*4)),
		)
	}
	opt = append(opt, grpc.WithBackoffMaxDelay(grpc.DefaultBackoffConfig.MaxDelay))

	cc, err := grpc.Dial(e.ServerAddress, opt...)
	if err != nil {
		rkentry.ShutdownWithError(errors.New("启动代理失败"))
	}
	return cc
}

func (e GRPCWebEntry) buildGrpcProxyServer(backendConn *grpc.ClientConn, logger *logrus.Entry) *grpc.Server {
	// gRPC-wide changes.
	grpc.EnableTracing = true
	grpc_logrus.ReplaceGrpcLogger(logger)

	// gRPC proxy logic.
	director := func(ctx context.Context, fullMethodName string) (context.Context, *grpc.ClientConn, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		outCtx, _ := context.WithCancel(ctx)
		mdCopy := md.Copy()
		delete(mdCopy, "user-agent")
		// If this header is present in the request from the web client,
		// the actual connection to the backend will not be established.
		// https://github.com/improbable-eng/grpc-web/issues/568
		delete(mdCopy, "connection")
		outCtx = metadata.NewOutgoingContext(outCtx, mdCopy)
		return outCtx, backendConn, nil
	}

	// Server with logging and monitoring enabled.
	return grpc.NewServer(
		grpc.CustomCodec(proxy.Codec()), // needed for proxy to function.
		grpc.UnknownServiceHandler(proxy.TransparentHandler(director)),
		grpc.MaxRecvMsgSize(e.MaxCallRecvMsgSize),
		grpc_middleware.WithUnaryServerChain(
			grpc_logrus.UnaryServerInterceptor(logger),
			grpc_prometheus.UnaryServerInterceptor,
		),
		grpc_middleware.WithStreamServerChain(
			grpc_logrus.StreamServerInterceptor(logger),
			grpc_prometheus.StreamServerInterceptor,
		),
	)
}

func (e GRPCWebEntry) buildServer(wrappedGrpc *grpcweb.WrappedGrpcServer, handler http.Handler) *http.Server {
	return &http.Server{
		WriteTimeout: e.HttpMaxWriteTimeout,
		ReadTimeout:  e.HttpMaxReadTimeout,
		Handler:      handler,
	}
}

func (e GRPCWebEntry) buildListenerOrFail(name string, port int) net.Listener {
	addr := fmt.Sprintf("%s:%d", e.BindAddress, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("failed listening for '%v' on %v: %v", name, port, err)
	}
	return conntrack.NewListener(listener,
		conntrack.TrackWithName(name),
		conntrack.TrackWithTcpKeepAlive(20*time.Second),
		conntrack.TrackWithTracing(),
	)
}

func serveServer(server *http.Server, listener net.Listener, name string, errChan chan error) {
	go func() {
		logrus.Infof("listening for %s on: %v", name, listener.Addr().String())
		if err := server.Serve(listener); err != nil {
			errChan <- fmt.Errorf("%s server error: %v", name, err)
		}
	}()
}

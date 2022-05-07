package rkgrpcwebproxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rookie-ninja/rk-entry/v2/entry"
	rkgrpc "github.com/rookie-ninja/rk-grpc/v2/boot"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/trace"
	"net/http"
	"time"
)

const (
	GrpcWebEntryType = "gRPCWebEntry"
)

func init() {
	rkentry.RegisterEntryRegFunc(RegisterGRPCWebYAML)
}

type GRPCWebEntry struct {
	Name        string `yaml:"name" json:"name"`
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	Description string `yaml:"description" json:"description"`
	Type        string `yaml:"type" json:"type"`

	LoggerEntry *rkentry.LoggerEntry `json:"-" yaml:"-"`
	GrpcEntry   *rkgrpc.GrpcEntry    `yaml:"-" json:"-"`
	CertEntry   *rkentry.CertEntry   `json:"-" yaml:"-"`

	ServerAddress string `yaml:"serverAddress" json:"server_address"`
	BindAddress   string `yaml:"bindAddress" json:"bind_address"`
	BindPort      int    `yaml:"bindPort" json:"bind_port"`

	MaxCallRecvMsgSize  int           `yaml:"maxCallRecvMsgSize" json:"max_call_recv_msg_size"`
	AllowedOrigins      []string      `yaml:"allowedOrigins" json:"allowed_origins"`
	AllowedHeaders      []string      `yaml:"allowedHeaders" json:"allowed_headers"`
	Websockets          *Websockets   `yaml:"websockets" json:"websockets"`
	Debug               bool          `yaml:"debug" json:"debug"`
	HttpMaxWriteTimeout time.Duration `yaml:"http_max_write_timeout" json:"http_max_write_timeout"`
	HttpMaxReadTimeout  time.Duration `yaml:"http_max_read_timeout" json:"http_max_read_timeout"`
}

type GRPCWebEntryOption func(*GRPCWebEntry)

func WithDebug(debug bool) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.Debug = debug
	}
}

func GetGRPCWebEntry(name string) *GRPCWebEntry {
	if raw := rkentry.GlobalAppCtx.GetEntry(GrpcWebEntryType, name); raw != nil {
		if res, ok := raw.(*GRPCWebEntry); ok {
			return res
		}
	}
	return nil
}

type BootConfig struct {
	GrpcWeb []struct {
		Name        string `yaml:"name" json:"name"`
		Enabled     bool   `yaml:"enabled" json:"enabled"`
		Description string `yaml:"description" json:"description"`
		GrpcEntry   string `yaml:"grpcEntry" json:"grpc_entry"`
		CertEntry   string `yaml:"certEntry" json:"cert_entry"`

		ServerAddress string `yaml:"serverAddress" json:"server_address"`

		BindAddress string `yaml:"bindAddress" json:"bind_address"`
		BindPort    int    `yaml:"bindPort" json:"bind_port"`

		MaxCallRecvMsgSize  int           `yaml:"maxCallRecvMsgSize" json:"max_call_recv_msg_size"`
		AllowedOrigins      []string      `yaml:"allowedOrigins" json:"allowed_origins"`
		AllowedHeaders      []string      `yaml:"allowedHeaders" json:"allowed_headers"`
		Debug               bool          `yaml:"debug" json:"debug"`
		HttpMaxWriteTimeout time.Duration `yaml:"http_max_write_timeout" json:"http_max_write_timeout"`
		HttpMaxReadTimeout  time.Duration `yaml:"http_max_read_timeout" json:"http_max_read_timeout"`
		Websockets          *Websockets   `yaml:"websockets" json:"websockets"`
	} `yaml:"grpcweb" json:"grpcweb"`
}

func RegisterGRPCWebYAML(raw []byte) map[string]rkentry.Entry {
	res := make(map[string]rkentry.Entry)
	config := &BootConfig{}
	rkentry.UnmarshalBootYAML(raw, config)

	for i := range config.GrpcWeb {
		element := config.GrpcWeb[i]
		if !element.Enabled {
			continue
		}
		if element.Enabled {
			entry := RegisterGRPCWebEntry(
				WithName(element.Name),
				WithDescription(element.Description),
				WithGrpcEntry(element.GrpcEntry),
				WithCertEntry(element.CertEntry),
				WithServerAddress(element.ServerAddress),
				WithBindAddress(element.BindAddress),
				WithBindPort(element.BindPort),
			)
			res[element.Name] = entry
		}
	}
	return res
}

func RegisterGRPCWebEntry(opts ...GRPCWebEntryOption) *GRPCWebEntry {
	entry := &GRPCWebEntry{
		LoggerEntry:        rkentry.NewLoggerEntryStdout(),
		Type:               GrpcWebEntryType,
		MaxCallRecvMsgSize: 1024 * 1024 * 4,
		Debug:              false,
		AllowedOrigins:     []string{},
		AllowedHeaders:     []string{},
	}
	for i := range opts {
		opts[i](entry)
	}

	fmt.Println("RegisterGRPCWebEntry", entry)

	rkentry.GlobalAppCtx.AddEntry(entry)
	return entry
}

func (e GRPCWebEntry) Bootstrap(ctx context.Context) {
	go e.Start()
}

func (e GRPCWebEntry) Start() {
	if e.GrpcEntry != nil {
		e.ServerAddress = fmt.Sprintf("localhost:%d", e.GrpcEntry.Port)
	}

	e.LoggerEntry.Info(fmt.Sprintf("gRPC web start with %s", e.ServerAddress))

	if len(e.ServerAddress) < 1 {
		rkentry.ShutdownWithError(errors.New("缺少必要参数 serverAddress"))
	}

	backendConn := e.dialBackendOrFail()
	grpcServer := e.buildGrpcProxyServer(backendConn, logrus.NewEntry(logrus.StandardLogger()))
	errChan := make(chan error)
	allowedOrigins := makeAllowedOrigins(e.AllowedOrigins)
	options := []grpcweb.Option{
		grpcweb.WithCorsForRegisteredEndpointsOnly(false),
		grpcweb.WithOriginFunc(makeHttpOriginFunc(allowedOrigins)),
	}
	if e.Websockets != nil {
		rkentry.LoggerEntryStdout.Info("GRPCWebEntry using websockets")
		options = append(
			options,
			grpcweb.WithWebsockets(true),
			grpcweb.WithWebsocketOriginFunc(makeWebsocketOriginFunc(allowedOrigins)),
		)
		if e.Websockets.PingInterval >= time.Second {
			logrus.Infof("websocket keepalive pinging enabled, the timeout interval is %s", e.Websockets.PingInterval.String())
		}
		if e.Websockets.ReadLimit > 0 {
			options = append(options, grpcweb.WithWebsocketsMessageReadLimit(e.Websockets.ReadLimit))
		}

		options = append(
			options,
			grpcweb.WithWebsocketPingInterval(e.Websockets.PingInterval),
		)
	}
	if len(e.AllowedHeaders) > 0 {
		options = append(
			options,
			grpcweb.WithAllowedRequestHeaders(e.AllowedHeaders),
		)
	}
	wrappedGrpc := grpcweb.WrapServer(grpcServer, options...)
	serveMux := http.NewServeMux()
	serveMux.Handle("/", wrappedGrpc)
	if e.Debug {
		serveMux.Handle("/metrics", promhttp.Handler())
		serveMux.HandleFunc("/debug/requests", func(resp http.ResponseWriter, req *http.Request) {
			trace.Traces(resp, req)
		})
		serveMux.HandleFunc("/debug/events", func(resp http.ResponseWriter, req *http.Request) {
			trace.Events(resp, req)
		})
	}
	server := e.buildServer(wrappedGrpc, serveMux)
	serverListener := e.buildListenerOrFail("http", e.BindPort)
	if e.CertEntry != nil {
		serverListener = tls.NewListener(serverListener, &tls.Config{
			InsecureSkipVerify: true,
			Certificates:       []tls.Certificate{*e.CertEntry.Certificate},
		})
	}
	serveServer(server, serverListener, "http", errChan)

	e.LoggerEntry.Info(fmt.Sprintf("server address %s:%d", e.BindAddress, e.BindPort))
}

func (e GRPCWebEntry) Interrupt(ctx context.Context) {
}

func (e GRPCWebEntry) GetName() string {
	return e.Name
}

func (e GRPCWebEntry) GetType() string {
	return GrpcWebEntryType
}

func (e GRPCWebEntry) GetDescription() string {
	return e.Description
}

func (e GRPCWebEntry) String() string {
	bytes, _ := json.Marshal(e)
	return string(bytes)
}

func WithName(name string) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.Name = name
	}
}

func WithDescription(description string) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.Description = description
	}
}

func WithGrpcEntry(grpcEntry string) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.GrpcEntry = rkgrpc.GetGrpcEntry(grpcEntry)
	}
}

func WithCertEntry(certEntry string) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.CertEntry = rkentry.GlobalAppCtx.GetCertEntry(certEntry)
	}
}

func WithMaxCallRecvMsgSize(maxCallRecvMsgSize int) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		if maxCallRecvMsgSize > 0 {
			entry.MaxCallRecvMsgSize = maxCallRecvMsgSize
		}
	}
}

func WithAllowedOrigins(allowedOrigins []string) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.AllowedOrigins = allowedOrigins
	}
}

func WithAllowedHeaders(allowedHeaders []string) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.AllowedHeaders = allowedHeaders
	}
}

func WithWebsockets(websockets *Websockets) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.Websockets = websockets
	}
}

func WithHttpMaxWriteTimeout(time time.Duration) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.HttpMaxWriteTimeout = time
	}
}

func WithHttpMaxReadTimeout(time time.Duration) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.HttpMaxReadTimeout = time
	}
}

func WithBindAddress(address string) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.BindAddress = address
	}
}

func WithBindPort(port int) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.BindPort = port
	}
}

func WithServerAddress(serverAddress string) GRPCWebEntryOption {
	return func(entry *GRPCWebEntry) {
		entry.ServerAddress = serverAddress
	}
}

// Command documents-service is the gRPC office/text-document parsing microservice
// (CON-280). It serves documents.v1.DocumentsService (a single client-streaming
// Parse RPC) plus a standard grpc.health.v1 endpoint. It is internal-only — the
// Ogen API reaches it over the Railway private network (plaintext h2c). Pure Go,
// no CGO, so it ships as a CGO_ENABLED=0 static binary.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	documentsv1 "github.com/ogen-app/document-service/gen/documents/v1"
	"github.com/ogen-app/document-service/internal/config"
	"github.com/ogen-app/document-service/internal/docengine"
	"github.com/ogen-app/document-service/internal/logging"
	"github.com/ogen-app/document-service/internal/runtimetune"
	"github.com/ogen-app/document-service/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Pre-logger: config drives the logger's level/format, so a load failure
		// can only report through the stdlib default.
		log.Fatalf("documents-service: config: %v", err)
	}

	logger := logging.New(cfg)

	// Bound the Go heap to the container and derive GOMEMLIMIT from the cgroup so
	// RSS tracks real usage. All working memory here is Go-managed (no CGO).
	runtimetune.Apply(logger, cfg)

	engine := docengine.New()

	lis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("listen", "component", "boot", "addr", cfg.Listen, "err", err)
		os.Exit(1)
	}

	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(logging.UnaryServerInterceptor(logger)),
		grpc.ChainStreamInterceptor(logging.StreamServerInterceptor(logger)),
	)
	documentsv1.RegisterDocumentsServiceServer(srv, server.New(engine, cfg.MaxConcurrent))

	// Standard grpc.health.v1 — set BOTH the service name and "" (overall),
	// because grpc_health_probe with no -service checks the empty service.
	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	hs.SetServingStatus("documents.v1.DocumentsService", healthpb.HealthCheckResponse_SERVING)
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down", "component", "boot")
		srv.GracefulStop()
	}()

	logger.Info("listening", "component", "boot", "addr", cfg.Listen, "max_concurrent", cfg.MaxConcurrent)
	if err := srv.Serve(lis); err != nil {
		logger.Error("serve", "component", "boot", "err", err)
		os.Exit(1)
	}
}

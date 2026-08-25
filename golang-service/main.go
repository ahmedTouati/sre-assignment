// Token Service - gRPC server with observability
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pb "token-service/proto"
)

var (
	tokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tokens_minted_total",
		Help: "Total tokens minted",
	}, []string{"status"})
	mintLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "token_mint_duration_seconds",
		Help:    "Token minting latency",
		Buckets: prometheus.DefBuckets,
	})
	logger *slog.Logger
)

func initOtel() func() {
	ctx := context.Background()

	// Set W3C Trace Context propagator for cross-service trace correlation
	otel.SetTextMapPropagator(propagation.TraceContext{})

	res, _ := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("go-token-service")),
	)

	// Initialize tracing
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res))

	// Initialize logging
	var lp *sdklog.LoggerProvider

	// Only export to OTLP if endpoint is configured
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		// Trace exporter
		traceExporter, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithTLSCredentials(insecure.NewCredentials()),
		)
		if err == nil {
			tp = sdktrace.NewTracerProvider(
				sdktrace.WithBatcher(traceExporter),
				sdktrace.WithResource(res),
			)
		}

		// Log exporter
		logExporter, err := otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(endpoint),
			otlploggrpc.WithTLSCredentials(insecure.NewCredentials()),
		)
		if err == nil {
			lp = sdklog.NewLoggerProvider(
				sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
				sdklog.WithResource(res),
			)
		}
	}

	// Fallback to stdout for logs if no OTLP endpoint
	if lp == nil {
		stdoutExporter, _ := stdoutlog.New()
		lp = sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewBatchProcessor(stdoutExporter)),
			sdklog.WithResource(res),
		)
	}

	otel.SetTracerProvider(tp)
	global.SetLoggerProvider(lp)

	// Create slog logger with OTel bridge
	logger = otelslog.NewLogger("go-token-service")

	return func() {
		tp.Shutdown(ctx)
		lp.Shutdown(ctx)
	}
}

type server struct {
	pb.UnimplementedTokenServiceServer
	rdb *redis.Client
}

func (s *server) MintToken(ctx context.Context, req *pb.MintTokenRequest) (*pb.MintTokenResponse, error) {
	start := time.Now()
	logger.InfoContext(ctx, "minting token", "user", req.UserEmail)

	bytes := make([]byte, 32)
	rand.Read(bytes)
	token := hex.EncodeToString(bytes)

	if err := s.rdb.Set(ctx, "token:"+token, req.UserEmail, time.Duration(req.TtlSeconds)*time.Second).Err(); err != nil {
		tokensTotal.WithLabelValues("error").Inc()
		logger.ErrorContext(ctx, "redis error", "error", err.Error())
		return nil, err
	}

	tokensTotal.WithLabelValues("success").Inc()
	mintLatency.Observe(time.Since(start).Seconds())
	logger.InfoContext(ctx, "token minted", "user", req.UserEmail)
	return &pb.MintTokenResponse{Token: token}, nil
}

func (s *server) ValidateToken(ctx context.Context, req *pb.ValidateTokenRequest) (*pb.ValidateTokenResponse, error) {
	email, err := s.rdb.Get(ctx, "token:"+req.Token).Result()
	if err != nil {
		return &pb.ValidateTokenResponse{Valid: false}, nil
	}
	return &pb.ValidateTokenResponse{Valid: true, UserEmail: email}, nil
}

func main() {
	cleanup := initOtel()
	defer cleanup()

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisPassword := os.Getenv("REDIS_PASSWORD")

	// Redis client with OpenTelemetry instrumentation
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		// Disable maintenance notifications (Redis Cloud feature not available in standard Redis)
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
	})
	if err := redisotel.InstrumentTracing(rdb); err != nil {
		panic(err)
	}

	// Metrics endpoint
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.ListenAndServe(":9090", nil)
	}()

	lis, _ := net.Listen("tcp", ":50051")
	// gRPC server with OpenTelemetry auto-instrumentation
	s := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	pb.RegisterTokenServiceServer(s, &server{rdb: rdb})

	logger.Info("token service starting", "grpc_port", 50051, "metrics_port", 9090)
	s.Serve(lis)
}

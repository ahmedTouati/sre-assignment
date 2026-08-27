package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
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
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	pb "token-service/proto"
)

var (
	tokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tokens_minted_total",
		Help: "Total tokens minted",
	}, []string{"status"})
	mintLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "token_mint_duration_seconds",
		Help:    "Token minting duration",
		Buckets: prometheus.DefBuckets,
	})
	grpcRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "grpc_server_requests_total",
		Help: "Total gRPC requests handled",
	}, []string{"method", "code"})
	grpcRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "grpc_server_request_duration_seconds",
		Help:    "gRPC request duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "code"})
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests handled",
	}, []string{"method", "status", "handler"})
	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "handler"})
	redisQueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "redis_queue_depth",
		Help: "Current direct Redis commands awaiting completion",
	})
	redisOperationDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "redis_operation_duration_seconds",
		Help:    "Redis operation duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation", "status"})
	logger *slog.Logger
)

const serviceName = "go-token-service"

type redisLogAdapter struct{}

func (redisLogAdapter) Printf(ctx context.Context, format string, args ...any) {
	logger.ErrorContext(ctx, "redis client error",
		slog.String("event", "dependency_error"),
		slog.String("dependency", "redis"),
		slog.String("error", fmt.Sprintf(format, args...)),
	)
}

func initOtel() func() {
	ctx := context.Background()
	bootstrapLogger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	otel.SetTextMapPropagator(propagation.TraceContext{})

	res, _ := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)

	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res))
	stdoutExporter, err := stdoutlog.New()
	if err != nil {
		bootstrapLogger.Error("stdout log exporter initialization failed",
			slog.String("event", "telemetry_exporter_init_failed"),
			slog.String("signal", "logs"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	logOptions := []sdklog.LoggerProviderOption{
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(stdoutExporter)),
	}

	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		traceExporter, traceErr := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithTLSCredentials(insecure.NewCredentials()),
		)
		if traceErr != nil {
			bootstrapLogger.Error("telemetry exporter initialization failed",
				slog.String("event", "telemetry_exporter_init_failed"),
				slog.String("dependency", "otel_collector"),
				slog.String("signal", "traces"),
				slog.String("error", traceErr.Error()),
			)
		} else {
			tp = sdktrace.NewTracerProvider(
				sdktrace.WithBatcher(traceExporter),
				sdktrace.WithResource(res),
			)
		}

		logExporter, logErr := otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(endpoint),
			otlploggrpc.WithTLSCredentials(insecure.NewCredentials()),
		)
		if logErr != nil {
			bootstrapLogger.Error("telemetry exporter initialization failed",
				slog.String("event", "telemetry_exporter_init_failed"),
				slog.String("dependency", "otel_collector"),
				slog.String("signal", "logs"),
				slog.String("error", logErr.Error()),
			)
		} else {
			logOptions = append(
				logOptions,
				sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
			)
		}
	}

	lp := sdklog.NewLoggerProvider(logOptions...)
	otel.SetTracerProvider(tp)
	global.SetLoggerProvider(lp)
	logger = otelslog.NewLogger(serviceName)

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := tp.Shutdown(shutdownCtx); err != nil {
			bootstrapLogger.Error("telemetry provider shutdown failed",
				slog.String("event", "telemetry_shutdown_failed"),
				slog.String("dependency", "otel_collector"),
				slog.String("signal", "traces"),
				slog.String("error", err.Error()),
			)
		}
		if err := lp.Shutdown(shutdownCtx); err != nil {
			bootstrapLogger.Error("telemetry provider shutdown failed",
				slog.String("event", "telemetry_shutdown_failed"),
				slog.String("dependency", "otel_collector"),
				slog.String("signal", "logs"),
				slog.String("error", err.Error()),
			)
		}
	}
}

type server struct {
	pb.UnimplementedTokenServiceServer
	rdb *redis.Client
}

func grpcMetricsInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	started := time.Now()
	response, err := handler(ctx, req)
	code := status.Code(err).String()

	grpcRequestsTotal.WithLabelValues(info.FullMethod, code).Inc()
	grpcRequestDuration.WithLabelValues(info.FullMethod, code).Observe(time.Since(started).Seconds())
	return response, err
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(body)
}

func instrumentHTTP(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(recorder, r)
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}

		status := strconv.Itoa(recorder.status/100) + "xx"
		httpRequestsTotal.WithLabelValues(r.Method, status, route).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(started).Seconds())
	})
}

func observeRedisOperation(operation string, run func() error) error {
	redisQueueDepth.Inc()
	defer redisQueueDepth.Dec()

	started := time.Now()
	err := run()
	operationStatus := "success"
	if errors.Is(err, redis.Nil) {
		operationStatus = "miss"
	} else if err != nil {
		operationStatus = "error"
	}
	redisOperationDuration.WithLabelValues(operation, operationStatus).Observe(time.Since(started).Seconds())
	return err
}

func writeStatus(w http.ResponseWriter, statusCode int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"status": status})
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, "healthy")
}

func readinessHandler(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()

		if err := rdb.Ping(ctx).Err(); err != nil {
			writeStatus(w, http.StatusServiceUnavailable, "not ready")
			return
		}

		writeStatus(w, http.StatusOK, "ready")
	}
}

func (s *server) MintToken(ctx context.Context, req *pb.MintTokenRequest) (*pb.MintTokenResponse, error) {
	start := time.Now()
	logger.InfoContext(ctx, "token mint started",
		slog.String("event", "token_mint_started"),
		slog.Int("permission_count", len(req.Permissions)),
		slog.Int64("ttl_seconds", int64(req.TtlSeconds)),
	)

	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		tokensTotal.WithLabelValues("error").Inc()
		logger.ErrorContext(ctx, "token mint failed",
			slog.String("event", "token_mint_failed"),
			slog.String("dependency", "system_random"),
			slog.String("operation", "read"),
			slog.String("error", err.Error()),
		)
		return nil, status.Error(codes.Internal, "failed to generate token")
	}
	token := hex.EncodeToString(bytes)

	err := observeRedisOperation("set", func() error {
		return s.rdb.Set(ctx, "token:"+token, req.UserEmail, time.Duration(req.TtlSeconds)*time.Second).Err()
	})
	if err != nil {
		tokensTotal.WithLabelValues("error").Inc()
		logger.ErrorContext(ctx, "token mint failed",
			slog.String("event", "token_mint_failed"),
			slog.String("dependency", "redis"),
			slog.String("operation", "set"),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("error", err.Error()),
		)
		return nil, err
	}

	tokensTotal.WithLabelValues("success").Inc()
	mintLatency.Observe(time.Since(start).Seconds())
	logger.InfoContext(ctx, "token mint completed",
		slog.String("event", "token_mint_completed"),
		slog.String("dependency", "redis"),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	)
	return &pb.MintTokenResponse{Token: token}, nil
}

func (s *server) ValidateToken(ctx context.Context, req *pb.ValidateTokenRequest) (*pb.ValidateTokenResponse, error) {
	start := time.Now()
	var email string
	err := observeRedisOperation("get", func() error {
		var redisErr error
		email, redisErr = s.rdb.Get(ctx, "token:"+req.Token).Result()
		return redisErr
	})
	if errors.Is(err, redis.Nil) {
		logger.InfoContext(ctx, "token validation completed",
			slog.String("event", "token_validation_completed"),
			slog.String("dependency", "redis"),
			slog.Bool("valid", false),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
		return &pb.ValidateTokenResponse{Valid: false}, nil
	}
	if err != nil {
		logger.ErrorContext(ctx, "token validation failed",
			slog.String("event", "token_validation_failed"),
			slog.String("dependency", "redis"),
			slog.String("operation", "get"),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("error", err.Error()),
		)
		return &pb.ValidateTokenResponse{Valid: false}, nil
	}
	logger.InfoContext(ctx, "token validation completed",
		slog.String("event", "token_validation_completed"),
		slog.String("dependency", "redis"),
		slog.Bool("valid", true),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	)
	return &pb.ValidateTokenResponse{Valid: true, UserEmail: email}, nil
}

func main() {
	cleanup := initOtel()
	defer cleanup()
	redis.SetLogger(redisLogAdapter{})

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisPassword := os.Getenv("REDIS_PASSWORD")

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
	})
	if err := redisotel.InstrumentTracing(rdb); err != nil {
		logger.Error("dependency instrumentation failed",
			slog.String("event", "dependency_instrumentation_failed"),
			slog.String("dependency", "redis"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		logger.Error("server listener failed",
			slog.String("event", "server_listener_failed"),
			slog.String("server", "grpc"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/health", instrumentHTTP("/health", http.HandlerFunc(healthHandler)))
	mux.Handle("/ready", instrumentHTTP("/ready", readinessHandler(rdb)))
	httpServer := &http.Server{
		Addr:              ":9090",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server stopped unexpectedly",
				slog.String("event", "server_failed"),
				slog.String("server", "http"),
				slog.String("error", err.Error()),
			)
			os.Exit(1)
		}
	}()

	s := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.UnaryInterceptor(grpcMetricsInterceptor),
	)
	pb.RegisterTokenServiceServer(s, &server{rdb: rdb})

	logger.Info("token service started",
		slog.String("event", "service_started"),
		slog.Int("grpc_port", 50051),
		slog.Int("http_port", 9090),
	)
	if err := s.Serve(lis); err != nil {
		logger.Error("server stopped unexpectedly",
			slog.String("event", "server_failed"),
			slog.String("server", "grpc"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/PolarishT/sales-agent/ent"
	"github.com/PolarishT/sales-agent/internal/agent"
	"github.com/PolarishT/sales-agent/internal/config"
	httpapi "github.com/PolarishT/sales-agent/internal/http"
	"github.com/PolarishT/sales-agent/internal/platform/bigmodel"
	databaseplatform "github.com/PolarishT/sales-agent/internal/platform/database"
	"github.com/PolarishT/sales-agent/internal/rag/ingestion"
	"github.com/PolarishT/sales-agent/internal/rag/markdown"
	"github.com/PolarishT/sales-agent/internal/rag/splitter"
	_ "github.com/lib/pq"
)

func main() {
	if err := run(); err != nil {
		slog.Error("服务启动失败", "error", err)
		os.Exit(1)
	}
}

func run() error {
	settings, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}
	configureLogger(settings.LogLevel)

	client, err := ent.Open("postgres", settings.DatabaseURL)
	if err != nil {
		return fmt.Errorf("打开 PostgreSQL Ent 客户端: %w", err)
	}
	defer client.Close()

	ingestionRepository, err := databaseplatform.NewIngestionRepository(client)
	if err != nil {
		return fmt.Errorf("创建 RAG 导入仓储: %w", err)
	}
	parser := markdown.NewParser()
	filter := markdown.NewFilter()
	normalizer := markdown.NewNormalizer()
	chunkSplitter := splitter.New()
	embedder, err := bigmodel.NewEmbedder(bigmodel.Config{
		BaseURL:    settings.ZhipuBaseURL,
		APIKey:     settings.ZhipuAPIKey,
		Model:      settings.ZhipuEmbeddingModel,
		Dimensions: settings.ZhipuEmbeddingDimensions,
		BatchSize:  settings.ZhipuEmbeddingBatchSize,
		Timeout:    settings.ZhipuEmbeddingTimeout,
		MaxRetries: 2,
	})
	if err != nil {
		return fmt.Errorf("创建智谱 Embedding 客户端: %w", err)
	}
	pipeline, err := ingestion.NewPipeline(
		ingestionRepository,
		parser,
		filter,
		normalizer,
		chunkSplitter,
		embedder,
	)
	if err != nil {
		return fmt.Errorf("创建 RAG 导入流水线: %w", err)
	}
	executor, err := ingestion.NewExecutor(
		settings.RAGIngestionWorkers,
		settings.RAGIngestionQueueCapacity,
		settings.RAGIngestionTimeout,
		pipeline,
		ingestionRepository,
	)
	if err != nil {
		return fmt.Errorf("创建 RAG 导入执行器: %w", err)
	}
	ingestionService, err := ingestion.NewService(
		ingestionRepository,
		executor,
		settings.RAGMaxUploadBytes,
	)
	if err != nil {
		return fmt.Errorf("创建 RAG 导入服务: %w", err)
	}

	graph, err := agent.NewGraph(context.Background())
	if err != nil {
		return err
	}

	executor.Start(context.Background())
	executorStopped := false
	defer func() {
		if executorStopped {
			return
		}
		shutdownContext, cancelShutdown := context.WithTimeout(
			context.Background(),
			settings.ShutdownTimeout,
		)
		defer cancelShutdown()
		_ = executor.Shutdown(shutdownContext)
	}()

	listener, err := openListener(settings.Address)
	if err != nil {
		return err
	}
	defer listener.Close()

	h := httpapi.NewServer(httpapi.Options{
		Address:         settings.Address,
		RequestTimeout:  settings.RequestTimeout,
		ShutdownTimeout: settings.ShutdownTimeout,
		Listener:        listener,
		Dependencies: httpapi.Dependencies{
			HealthChecker:    databaseplatform.NewReadiness(client),
			AgentRunner:      graph,
			IngestionService: ingestionService,
			MaxUploadBytes:   settings.RAGMaxUploadBytes,
			ReadinessTimeout: settings.DatabaseConnectTimeout,
		},
	})
	register(h)

	slog.Info("导购后端已监听", "environment", settings.Environment, "address", listener.Addr().String())
	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	serveErr := serve(signalContext, h, executor, settings.ShutdownTimeout)
	executorStopped = true
	return serveErr
}

func openListener(address string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("监听地址 %q: %w", address, err)
	}
	return listener, nil
}

type runtimeServer interface {
	Run() error
	Shutdown(context.Context) error
}

type shutdownComponent interface {
	Shutdown(context.Context) error
}

func serve(
	ctx context.Context,
	h runtimeServer,
	worker shutdownComponent,
	shutdownTimeout time.Duration,
) error {
	runErrors := make(chan error, 1)
	go func() {
		runErrors <- h.Run()
	}()

	select {
	case err := <-runErrors:
		shutdownErr := shutdownRuntime(h, worker, shutdownTimeout)
		if err == nil {
			if shutdownErr != nil {
				return errors.Join(
					errors.New("Hertz 服务意外停止"),
					shutdownErr,
				)
			}
			return errors.New("Hertz 服务意外停止")
		}
		return errors.Join(
			fmt.Errorf("运行 Hertz 服务: %w", err),
			shutdownErr,
		)
	case <-ctx.Done():
		return shutdownRuntime(h, worker, shutdownTimeout)
	}
}

func shutdownRuntime(
	h shutdownComponent,
	worker shutdownComponent,
	shutdownTimeout time.Duration,
) error {
	shutdownContext, cancelShutdown := context.WithTimeout(
		context.Background(),
		shutdownTimeout,
	)
	defer cancelShutdown()

	httpErr := h.Shutdown(shutdownContext)
	workerErr := worker.Shutdown(shutdownContext)
	if httpErr != nil {
		httpErr = fmt.Errorf("关闭 Hertz 服务: %w", httpErr)
	}
	if workerErr != nil {
		workerErr = fmt.Errorf("关闭 RAG 导入 worker: %w", workerErr)
	}
	return errors.Join(httpErr, workerErr)
}

func configureLogger(rawLevel string) {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(rawLevel)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

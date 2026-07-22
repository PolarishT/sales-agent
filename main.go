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
	databaseplatform "github.com/PolarishT/sales-agent/internal/platform/database"
	"github.com/cloudwego/hertz/pkg/app/server"
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

	graph, err := agent.NewGraph(context.Background())
	if err != nil {
		return err
	}

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
			ReadinessTimeout: settings.DatabaseConnectTimeout,
		},
	})
	register(h)

	slog.Info("导购后端已监听", "environment", settings.Environment, "address", listener.Addr().String())
	signalContext, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	return serve(signalContext, h, settings.ShutdownTimeout)
}

func openListener(address string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("监听地址 %q: %w", address, err)
	}
	return listener, nil
}

func serve(ctx context.Context, h *server.Hertz, shutdownTimeout time.Duration) error {
	runErrors := make(chan error, 1)
	go func() {
		runErrors <- h.Run()
	}()

	select {
	case err := <-runErrors:
		if err == nil {
			return errors.New("Hertz 服务意外停止")
		}
		return fmt.Errorf("运行 Hertz 服务: %w", err)
	case <-ctx.Done():
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancelShutdown()
		if err := h.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("关闭 Hertz 服务: %w", err)
		}
		return nil
	}
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

package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddress     = ":3000"
	defaultEnvironment = "development"
	defaultLogLevel    = "info"
)

var (
	defaultDatabaseConnectTimeout = 5 * time.Second
	defaultRequestTimeout         = 30 * time.Second
	defaultGraphTimeout           = 60 * time.Second
	defaultShutdownTimeout        = 10 * time.Second
)

type Config struct {
	Environment            string
	Address                string
	DatabaseURL            string
	DatabaseConnectTimeout time.Duration
	RequestTimeout         time.Duration
	GraphTimeout           time.Duration
	ShutdownTimeout        time.Duration
	LogLevel               string
}

func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("必须提供环境变量读取函数")
	}
	environment := valueOr(getenv("APP_ENV"), valueOr(getenv("VERCEL_ENV"), defaultEnvironment))
	address, err := loadAddress(getenv)
	if err != nil {
		return Config{}, err
	}
	connectTimeout, err := durationValue(getenv("DB_CONNECT_TIMEOUT"), defaultDatabaseConnectTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("DB_CONNECT_TIMEOUT 配置错误: %w", err)
	}
	requestTimeout, err := durationValue(getenv("REQUEST_TIMEOUT"), defaultRequestTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT 配置错误: %w", err)
	}
	graphTimeout, err := durationValue(getenv("GRAPH_TIMEOUT"), defaultGraphTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("GRAPH_TIMEOUT 配置错误: %w", err)
	}
	shutdownTimeout, err := durationValue(getenv("SHUTDOWN_TIMEOUT"), defaultShutdownTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT 配置错误: %w", err)
	}
	databaseURL := strings.TrimSpace(getenv("DATABASE_URL"))
	if databaseURL == "" {
		return Config{}, errors.New("缺少必需的 DATABASE_URL")
	}
	return Config{
		Environment: environment, Address: address, DatabaseURL: databaseURL,
		DatabaseConnectTimeout: connectTimeout, RequestTimeout: requestTimeout,
		GraphTimeout: graphTimeout, ShutdownTimeout: shutdownTimeout,
		LogLevel: valueOr(getenv("LOG_LEVEL"), defaultLogLevel),
	}, nil
}

func loadAddress(getenv func(string) string) (string, error) {
	if port := strings.TrimSpace(getenv("PORT")); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("PORT 必须是 1 到 65535 之间的整数")
		}
		return ":" + port, nil
	}
	return valueOr(getenv("HTTP_ADDR"), defaultAddress), nil
}

func durationValue(raw string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, errors.New("必须是大于零的 Go duration，例如 5s 或 1m")
	}
	return value, nil
}

func valueOr(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

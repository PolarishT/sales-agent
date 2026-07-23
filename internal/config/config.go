package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddress                   = ":3000"
	defaultEnvironment               = "development"
	defaultLogLevel                  = "info"
	defaultRAGIngestionWorkers       = 1
	defaultRAGIngestionQueueCapacity = 8
	defaultRAGMaxUploadBytes         = int64(5 << 20)
	defaultZhipuBaseURL              = "https://open.bigmodel.cn/api/paas/v4"
	defaultZhipuEmbeddingModel       = "embedding-3"
	defaultZhipuEmbeddingDimensions  = 1024
	defaultZhipuEmbeddingBatchSize   = 32
)

var (
	defaultDatabaseConnectTimeout = 5 * time.Second
	defaultRequestTimeout         = 30 * time.Second
	defaultGraphTimeout           = 60 * time.Second
	defaultShutdownTimeout        = 10 * time.Second
	defaultRAGIngestionTimeout    = 10 * time.Minute
	defaultZhipuEmbeddingTimeout  = 30 * time.Second
)

type Config struct {
	Environment               string
	Address                   string
	DatabaseURL               string
	DatabaseConnectTimeout    time.Duration
	RequestTimeout            time.Duration
	GraphTimeout              time.Duration
	ShutdownTimeout           time.Duration
	LogLevel                  string
	RAGIngestionWorkers       int
	RAGIngestionQueueCapacity int
	RAGMaxUploadBytes         int64
	RAGIngestionTimeout       time.Duration
	ZhipuAPIKey               string
	ZhipuBaseURL              string
	ZhipuEmbeddingModel       string
	ZhipuEmbeddingDimensions  int
	ZhipuEmbeddingBatchSize   int
	ZhipuEmbeddingTimeout     time.Duration
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
	ragIngestionWorkers, err := positiveIntValue(getenv("RAG_INGESTION_WORKERS"), defaultRAGIngestionWorkers)
	if err != nil {
		return Config{}, fmt.Errorf("RAG_INGESTION_WORKERS 配置错误: %w", err)
	}
	ragIngestionQueueCapacity, err := positiveIntValue(getenv("RAG_INGESTION_QUEUE_CAPACITY"), defaultRAGIngestionQueueCapacity)
	if err != nil {
		return Config{}, fmt.Errorf("RAG_INGESTION_QUEUE_CAPACITY 配置错误: %w", err)
	}
	ragMaxUploadBytes, err := positiveInt64Value(getenv("RAG_MAX_UPLOAD_BYTES"), defaultRAGMaxUploadBytes)
	if err != nil {
		return Config{}, fmt.Errorf("RAG_MAX_UPLOAD_BYTES 配置错误: %w", err)
	}
	ragIngestionTimeout, err := durationValue(getenv("RAG_INGESTION_TIMEOUT"), defaultRAGIngestionTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("RAG_INGESTION_TIMEOUT 配置错误: %w", err)
	}
	zhipuEmbeddingDimensions, err := positiveIntValue(getenv("ZHIPU_EMBEDDING_DIMENSIONS"), defaultZhipuEmbeddingDimensions)
	if err != nil {
		return Config{}, fmt.Errorf("ZHIPU_EMBEDDING_DIMENSIONS 配置错误: %w", err)
	}
	if zhipuEmbeddingDimensions != defaultZhipuEmbeddingDimensions {
		return Config{}, errors.New("ZHIPU_EMBEDDING_DIMENSIONS 必须是 1024")
	}
	zhipuEmbeddingBatchSize, err := positiveIntValue(getenv("ZHIPU_EMBEDDING_BATCH_SIZE"), defaultZhipuEmbeddingBatchSize)
	if err != nil {
		return Config{}, fmt.Errorf("ZHIPU_EMBEDDING_BATCH_SIZE 配置错误: %w", err)
	}
	if zhipuEmbeddingBatchSize > 64 {
		return Config{}, errors.New("ZHIPU_EMBEDDING_BATCH_SIZE 必须介于 1 到 64 之间")
	}
	zhipuEmbeddingTimeout, err := durationValue(getenv("ZHIPU_EMBEDDING_TIMEOUT"), defaultZhipuEmbeddingTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("ZHIPU_EMBEDDING_TIMEOUT 配置错误: %w", err)
	}
	databaseURL := strings.TrimSpace(getenv("DATABASE_URL"))
	if databaseURL == "" {
		return Config{}, errors.New("缺少必需的 DATABASE_URL")
	}
	zhipuAPIKey := strings.TrimSpace(getenv("ZHIPU_API_KEY"))
	if zhipuAPIKey == "" {
		return Config{}, errors.New("缺少必需的 ZHIPU_API_KEY")
	}
	zhipuEmbeddingModel := valueOr(getenv("ZHIPU_EMBEDDING_MODEL"), defaultZhipuEmbeddingModel)
	if zhipuEmbeddingModel != defaultZhipuEmbeddingModel {
		return Config{}, errors.New("ZHIPU_EMBEDDING_MODEL 必须是 embedding-3")
	}
	return Config{
		Environment: environment, Address: address, DatabaseURL: databaseURL,
		DatabaseConnectTimeout: connectTimeout, RequestTimeout: requestTimeout,
		GraphTimeout: graphTimeout, ShutdownTimeout: shutdownTimeout,
		LogLevel:            valueOr(getenv("LOG_LEVEL"), defaultLogLevel),
		RAGIngestionWorkers: ragIngestionWorkers, RAGIngestionQueueCapacity: ragIngestionQueueCapacity,
		RAGMaxUploadBytes: ragMaxUploadBytes, RAGIngestionTimeout: ragIngestionTimeout,
		ZhipuAPIKey: zhipuAPIKey, ZhipuBaseURL: valueOr(getenv("ZHIPU_BASE_URL"), defaultZhipuBaseURL),
		ZhipuEmbeddingModel: zhipuEmbeddingModel, ZhipuEmbeddingDimensions: zhipuEmbeddingDimensions,
		ZhipuEmbeddingBatchSize: zhipuEmbeddingBatchSize, ZhipuEmbeddingTimeout: zhipuEmbeddingTimeout,
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

func positiveIntValue(raw string, fallback int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, errors.New("必须是大于零的整数")
	}
	return value, nil
}

func positiveInt64Value(raw string, fallback int64) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("必须是大于零的整数")
	}
	return value, nil
}

func valueOr(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

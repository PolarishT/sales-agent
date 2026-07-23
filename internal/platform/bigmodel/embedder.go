package bigmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/cloudwego/eino/components/embedding"
)

const (
	requiredModel          = "embedding-3"
	requiredDimensions     = 1024
	maxBatchSize           = 32
	maxConfiguredBatchSize = 64
	maxResponseBytes       = 2 << 20
	defaultMaxRetries      = 2
	maxRetries             = 2
	initialRetryDelay      = 100 * time.Millisecond
)

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Dimensions int
	BatchSize  int
	Timeout    time.Duration
	MaxRetries int
}

type Embedder struct {
	endpoint   string
	apiKey     string
	model      string
	dimensions int
	batchSize  int
	client     *http.Client
	maxRetries int
	sleep      func(context.Context, time.Duration) error
}

type embeddingPayload struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

type embeddingResponse struct {
	Data []wireResponseItem `json:"data"`
}

type wireResponseItem struct {
	Index     *int      `json:"index"`
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
}

type responseItem struct {
	Index     int       `json:"index"`
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
}

func NewEmbedder(config Config) (*Embedder, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, errors.New("智谱 API Key 不能为空")
	}
	if config.Model != requiredModel {
		return nil, errors.New("智谱 Embedding 模型必须是 embedding-3")
	}
	if config.Dimensions != requiredDimensions {
		return nil, errors.New("智谱 Embedding 维度必须是 1024")
	}
	if config.BatchSize < 1 || config.BatchSize > maxConfiguredBatchSize {
		return nil, errors.New("智谱 Embedding 批量大小必须介于 1 到 64 之间")
	}
	if config.Timeout <= 0 {
		return nil, errors.New("智谱 Embedding 超时必须大于零")
	}
	if config.MaxRetries < 0 || config.MaxRetries > maxRetries {
		return nil, errors.New("智谱 Embedding 重试次数必须介于 0 到 2 之间")
	}

	batchSize := config.BatchSize
	if batchSize > maxBatchSize {
		batchSize = maxBatchSize
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &Embedder{
		endpoint:   baseURL + "/embeddings",
		apiKey:     apiKey,
		model:      requiredModel,
		dimensions: requiredDimensions,
		batchSize:  batchSize,
		client: &http.Client{
			Timeout: config.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxRetries: config.MaxRetries,
		sleep:      sleepContext,
	}, nil
}

func (e *Embedder) EmbedStrings(
	ctx context.Context,
	texts []string,
	opts ...embedding.Option,
) ([][]float64, error) {
	// The vector space is fixed for persisted vector(1024) data. Eino call
	// options therefore cannot override the configured model.
	_ = opts
	if ctx == nil {
		return nil, embeddingFailure(errors.New("nil context"))
	}
	if len(texts) == 0 {
		return make([][]float64, 0), nil
	}

	result := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := start + e.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
	}
	return result, nil
}

func (e *Embedder) embedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	body, err := json.Marshal(embeddingPayload{
		Model:      e.model,
		Input:      texts,
		Dimensions: e.dimensions,
	})
	if err != nil {
		return nil, embeddingFailure(errors.New("encode embedding request"))
	}

	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, embeddingFailure(err)
		}

		items, retry, err := e.send(ctx, body, len(texts))
		if err == nil {
			return items, nil
		}
		if !retry || attempt == e.maxRetries {
			return nil, err
		}
		if err := e.waitBeforeRetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, embeddingFailure(errors.New("embedding retries exhausted"))
}

func (e *Embedder) send(
	ctx context.Context,
	body []byte,
	inputCount int,
) (vectors [][]float64, retry bool, returnedErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, embeddingFailure(errors.New("create embedding request"))
	}
	request.Header.Set("Authorization", "Bearer "+e.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := e.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, embeddingFailure(ctxErr)
		}
		return nil, true, embeddingFailure(err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		drainResponseBody(response.Body)
	}
	if response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode >= 500 && response.StatusCode <= 599 {
		return nil, true, embeddingFailure(statusError(response.StatusCode))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, false, embeddingFailure(statusError(response.StatusCode))
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, embeddingFailure(ctxErr)
		}
		return nil, true, embeddingFailure(errors.New("read embedding response"))
	}
	if len(raw) > maxResponseBytes {
		return nil, false, invalidResponse(errors.New("embedding response exceeds 2 MiB"))
	}

	var decoded embeddingResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false, invalidResponse(errors.New("decode embedding response"))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false, invalidResponse(errors.New("embedding response contains trailing data"))
	}
	items := make([]responseItem, len(decoded.Data))
	for index, item := range decoded.Data {
		if item.Index == nil {
			return nil, false, invalidResponse(errors.New("embedding response index missing"))
		}
		items[index] = responseItem{
			Index:     *item.Index,
			Object:    item.Object,
			Embedding: item.Embedding,
		}
	}
	vectors, err = validateResponse(items, inputCount, e.dimensions)
	if err != nil {
		return nil, false, err
	}
	return vectors, false, nil
}

func drainResponseBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxResponseBytes+1))
}

func validateResponse(items []responseItem, inputCount, dimensions int) ([][]float64, error) {
	if len(items) != inputCount {
		return nil, invalidResponse(errors.New("embedding response count mismatch"))
	}

	vectors := make([][]float64, inputCount)
	seen := make([]bool, inputCount)
	for _, item := range items {
		if item.Index < 0 || item.Index >= inputCount {
			return nil, invalidResponse(errors.New("embedding response index out of range"))
		}
		if seen[item.Index] {
			return nil, invalidResponse(errors.New("embedding response index duplicated"))
		}
		if len(item.Embedding) != dimensions {
			return nil, invalidResponse(errors.New("embedding response dimension mismatch"))
		}
		for _, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, invalidResponse(errors.New("embedding response contains non-finite value"))
			}
		}
		seen[item.Index] = true
		vectors[item.Index] = item.Embedding
	}
	for _, present := range seen {
		if !present {
			return nil, invalidResponse(errors.New("embedding response index missing"))
		}
	}
	return vectors, nil
}

func (e *Embedder) waitBeforeRetry(ctx context.Context, attempt int) error {
	delay := initialRetryDelay << attempt
	jitterLimit := delay / 4
	delay += time.Duration(rand.Int63n(int64(jitterLimit) + 1))
	if err := e.sleep(ctx, delay); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return embeddingFailure(ctxErr)
		}
		return embeddingFailure(err)
	}
	return nil
}

func validateBaseURL(baseURL string) error {
	if baseURL == "" {
		return errors.New("智谱 Base URL 不能为空")
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return errors.New("智谱 Base URL 无效")
	}
	return nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func statusError(status int) error {
	return fmt.Errorf("embedding endpoint returned HTTP status %d", status)
}

func embeddingFailure(err error) error {
	return domain.NewError(domain.CodeEmbeddingFailed, "文本向量生成失败", err)
}

func invalidResponse(err error) error {
	return domain.NewError(domain.CodeInvalidEmbeddingResponse, "Embedding 响应无效", err)
}

package ingestion

import (
	"context"
	"errors"
	"math"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/google/uuid"
)

const (
	defaultChunkSize      = 512
	defaultChunkOverlap   = 64
	embeddingVectorLength = 1024
)

type Pipeline struct {
	repository Repository
	parser     DocumentParser
	filter     DocumentFilter
	normalizer DocumentNormalizer
	splitter   ChunkSplitter
	embedder   TextEmbedder
}

func NewPipeline(
	repository Repository,
	parser DocumentParser,
	filter DocumentFilter,
	normalizer DocumentNormalizer,
	splitter ChunkSplitter,
	embedder TextEmbedder,
) (*Pipeline, error) {
	if repository == nil {
		return nil, errors.New("缺少 RAG 导入仓储")
	}
	if parser == nil {
		return nil, errors.New("缺少 Markdown 解析器")
	}
	if filter == nil {
		return nil, errors.New("缺少 Markdown 过滤器")
	}
	if normalizer == nil {
		return nil, errors.New("缺少 Markdown 规范化器")
	}
	if splitter == nil {
		return nil, errors.New("缺少文档切分器")
	}
	if embedder == nil {
		return nil, errors.New("缺少文本向量生成器")
	}
	return &Pipeline{
		repository: repository,
		parser:     parser,
		filter:     filter,
		normalizer: normalizer,
		splitter:   splitter,
		embedder:   embedder,
	}, nil
}

func (p *Pipeline) Run(ctx context.Context, ingestionID uuid.UUID) error {
	currentStage := domain.StageQueued
	upload, err := p.repository.LoadSource(ctx, ingestionID)
	if err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeDocumentStoreFailed, "加载导入文档失败"),
		)
	}
	if err := p.updateStage(ctx, ingestionID, domain.StageParsing); err != nil {
		return atStage(currentStage, err)
	}
	currentStage = domain.StageParsing

	parsed, err := p.parser.Parse(ctx, upload.Markdown)
	if err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeMarkdownParseFailed, "Markdown 解析失败"),
		)
	}
	if err := p.updateStage(ctx, ingestionID, domain.StageFiltering); err != nil {
		return atStage(currentStage, err)
	}
	currentStage = domain.StageFiltering

	filtered, err := p.filter.Apply(ctx, parsed)
	if err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeNoIndexableContent, "文档没有可索引内容"),
		)
	}
	if err := p.updateStage(ctx, ingestionID, domain.StageNormalizing); err != nil {
		return atStage(currentStage, err)
	}
	currentStage = domain.StageNormalizing

	normalized, err := p.normalizer.Normalize(ctx, filtered)
	if err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeNoIndexableContent, "文档规范化后没有可索引内容"),
		)
	}
	if err := p.updateStage(ctx, ingestionID, domain.StageChunking); err != nil {
		return atStage(currentStage, err)
	}
	currentStage = domain.StageChunking

	chunks, err := p.splitter.Split(ctx, normalized, domain.ChunkConfig{
		ChunkSize:    defaultChunkSize,
		ChunkOverlap: defaultChunkOverlap,
	})
	if err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeDocumentSplitFailed, "文档切分失败"),
		)
	}
	if len(chunks) == 0 {
		return atStage(
			currentStage,
			domain.NewError(domain.CodeDocumentSplitFailed, "文档切分失败", nil),
		)
	}
	if err := p.repository.UpdateProgress(ctx, ingestionID, len(chunks), 0); err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeDocumentStoreFailed, "更新导入进度失败"),
		)
	}
	if err := p.updateStage(ctx, ingestionID, domain.StageEmbedding); err != nil {
		return atStage(currentStage, err)
	}
	currentStage = domain.StageEmbedding

	texts := make([]string, len(chunks))
	for index := range chunks {
		texts[index] = chunks[index].EmbeddingContent
	}
	vectors, err := p.embedder.EmbedStrings(ctx, texts)
	if err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeEmbeddingFailed, "文本向量生成失败"),
		)
	}
	if err := validateVectors(vectors, len(chunks)); err != nil {
		return atStage(currentStage, err)
	}

	embedded := make([]domain.EmbeddedChunk, len(chunks))
	for index := range chunks {
		embedded[index] = domain.EmbeddedChunk{
			Chunk:  chunks[index],
			Vector: vectors[index],
		}
	}
	if err := p.repository.UpdateProgress(ctx, ingestionID, len(chunks), len(embedded)); err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeDocumentStoreFailed, "更新导入进度失败"),
		)
	}
	if err := p.updateStage(ctx, ingestionID, domain.StageStoring); err != nil {
		return atStage(currentStage, err)
	}
	currentStage = domain.StageStoring
	if err := p.repository.StoreAndActivate(ctx, ingestionID, embedded); err != nil {
		return atStage(
			currentStage,
			stableOr(err, domain.CodeDocumentStoreFailed, "保存导入文档失败"),
		)
	}
	return nil
}

func (p *Pipeline) updateStage(
	ctx context.Context,
	ingestionID uuid.UUID,
	stage domain.Stage,
) error {
	if err := p.repository.UpdateStage(ctx, ingestionID, domain.StatusRunning, stage); err != nil {
		return stableOr(err, domain.CodeDocumentStoreFailed, "更新导入阶段失败")
	}
	return nil
}

func validateVectors(vectors [][]float64, expected int) error {
	if len(vectors) != expected {
		return domain.NewError(
			domain.CodeInvalidEmbeddingResponse,
			"Embedding 响应数量无效",
			nil,
		)
	}
	for _, vector := range vectors {
		if len(vector) != embeddingVectorLength {
			return domain.NewError(
				domain.CodeInvalidEmbeddingResponse,
				"Embedding 响应维度无效",
				nil,
			)
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return domain.NewError(
					domain.CodeInvalidEmbeddingResponse,
					"Embedding 响应包含无效数值",
					nil,
				)
			}
		}
	}
	return nil
}

func stableOr(err error, code string, message string) error {
	var stable *domain.Error
	if errors.As(err, &stable) {
		return err
	}
	return domain.NewError(code, message, err)
}

type stagedError struct {
	stage domain.Stage
	err   error
}

func (e *stagedError) Error() string {
	return e.err.Error()
}

func (e *stagedError) Unwrap() error {
	return e.err
}

func atStage(stage domain.Stage, err error) error {
	if err == nil {
		return nil
	}
	var staged *stagedError
	if errors.As(err, &staged) {
		return err
	}
	return &stagedError{stage: stage, err: err}
}

func stageFromError(err error) domain.Stage {
	var staged *stagedError
	if errors.As(err, &staged) {
		return staged.stage
	}
	return domain.StageQueued
}

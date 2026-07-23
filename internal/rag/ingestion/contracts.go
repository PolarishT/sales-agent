package ingestion

import (
	"context"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/google/uuid"
)

type SubmissionDecision int

const (
	SubmissionCreate SubmissionDecision = iota
	SubmissionReuse
	SubmissionConflict
)

type Repository interface {
	InspectSubmission(context.Context, string, string) (domain.Task, SubmissionDecision, error)
	CreateQueued(context.Context, domain.Upload) (domain.Task, SubmissionDecision, error)
	GetTask(context.Context, uuid.UUID) (domain.Task, error)
	LoadSource(context.Context, uuid.UUID) (domain.Upload, error)
	UpdateStage(context.Context, uuid.UUID, domain.Status, domain.Stage) error
	UpdateProgress(context.Context, uuid.UUID, int, int) error
	MarkFailed(context.Context, uuid.UUID, domain.Stage, domain.Failure) error
	StoreAndActivate(context.Context, uuid.UUID, []domain.EmbeddedChunk) error
}

type DocumentParser interface {
	Parse(context.Context, []byte) (domain.ParsedDocument, error)
}

type DocumentFilter interface {
	Apply(context.Context, domain.ParsedDocument) (domain.ParsedDocument, error)
}

type DocumentNormalizer interface {
	Normalize(context.Context, domain.ParsedDocument) (domain.NormalizedDocument, error)
}

type ChunkSplitter interface {
	Split(context.Context, domain.NormalizedDocument, domain.ChunkConfig) ([]domain.Chunk, error)
}

type TextEmbedder interface {
	EmbedStrings(context.Context, []string, ...embedding.Option) ([][]float64, error)
}

package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Status string
type Stage string

const (
	StatusQueued    Status = "QUEUED"
	StatusRunning   Status = "RUNNING"
	StatusSucceeded Status = "SUCCEEDED"
	StatusFailed    Status = "FAILED"

	StageQueued      Stage = "QUEUED"
	StageParsing     Stage = "PARSING"
	StageFiltering   Stage = "FILTERING"
	StageNormalizing Stage = "NORMALIZING"
	StageChunking    Stage = "CHUNKING"
	StageEmbedding   Stage = "EMBEDDING"
	StageStoring     Stage = "STORING"
	StageCompleted   Stage = "COMPLETED"
)

const (
	CodeInvalidDocumentKey       = "INVALID_DOCUMENT_KEY"
	CodeFileRequired             = "FILE_REQUIRED"
	CodeUnsupportedFileType      = "UNSUPPORTED_FILE_TYPE"
	CodeFileTooLarge             = "FILE_TOO_LARGE"
	CodeInvalidMarkdownEncoding  = "INVALID_MARKDOWN_ENCODING"
	CodeEmptyDocument            = "EMPTY_DOCUMENT"
	CodeInvalidIngestionID       = "INVALID_INGESTION_ID"
	CodeIngestionInProgress      = "INGESTION_IN_PROGRESS"
	CodeIngestionNotFound        = "INGESTION_NOT_FOUND"
	CodeIngestionQueueFull       = "INGESTION_QUEUE_FULL"
	CodeIngestionUnavailable     = "INGESTION_UNAVAILABLE"
	CodeMarkdownParseFailed      = "MARKDOWN_PARSE_FAILED"
	CodeNoIndexableContent       = "NO_INDEXABLE_CONTENT"
	CodeDocumentSplitFailed      = "DOCUMENT_SPLIT_FAILED"
	CodeEmbeddingFailed          = "EMBEDDING_FAILED"
	CodeInvalidEmbeddingResponse = "INVALID_EMBEDDING_RESPONSE"
	CodeDocumentStoreFailed      = "DOCUMENT_STORE_FAILED"
	CodeProcessInterrupted       = "PROCESS_INTERRUPTED"
	CodeInternalProcessing       = "INTERNAL_PROCESSING_ERROR"
)

type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

func NewError(code, message string, err error) error {
	return &Error{Code: code, Message: message, Err: err}
}

func IsCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

type Failure struct {
	Code    string
	Message string
}

type Task struct {
	IngestionID        uuid.UUID
	DocumentKey        string
	Status             Status
	Stage              Stage
	SourceBytes        int64
	ChunkCount         int
	EmbeddedChunkCount int
	Failure            *Failure
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

type Submission struct {
	Task         Task
	Deduplicated bool
}

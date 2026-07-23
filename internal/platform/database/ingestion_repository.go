package database

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/PolarishT/sales-agent/ent"
	"github.com/PolarishT/sales-agent/ent/ragdocument"
	"github.com/PolarishT/sales-agent/ent/ragdocumentversion"
	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/PolarishT/sales-agent/internal/rag/ingestion"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

const embeddingDimensions = 1024

type IngestionRepository struct {
	client *ent.Client
	now    func() time.Time
}

func NewIngestionRepository(client *ent.Client) (*IngestionRepository, error) {
	if client == nil {
		return nil, errors.New("缺少 Ent 数据库客户端")
	}
	return &IngestionRepository{client: client, now: time.Now}, nil
}

func (repository *IngestionRepository) InspectSubmission(
	ctx context.Context,
	documentKey string,
	contentHash string,
) (domain.Task, ingestion.SubmissionDecision, error) {
	document, err := repository.client.RagDocument.
		Query().
		Where(ragdocument.DocumentKeyEQ(documentKey)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return domain.Task{}, ingestion.SubmissionCreate, nil
	}
	if err != nil {
		return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
	}

	versions, err := repository.client.RagDocumentVersion.
		Query().
		Where(
			ragdocumentversion.DocumentIDEQ(document.ID),
			ragdocumentversion.Or(
				ragdocumentversion.VersionEQ(document.CurrentVersion),
				ragdocumentversion.StatusIn(
					string(domain.StatusQueued),
					string(domain.StatusRunning),
				),
			),
		).
		All(ctx)
	if err != nil {
		return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
	}
	return decideSubmission(document, versions, contentHash)
}

func (repository *IngestionRepository) CreateQueued(
	ctx context.Context,
	upload domain.Upload,
) (domain.Task, ingestion.SubmissionDecision, error) {
	tx, err := repository.client.Tx(ctx)
	if err != nil {
		return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := repository.now()
	_, err = tx.RagDocument.
		Create().
		SetDocumentKey(upload.DocumentKey).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		OnConflictColumns(ragdocument.FieldDocumentKey).
		Ignore().
		ID(ctx)
	if err != nil {
		return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
	}

	document, err := tx.RagDocument.
		Query().
		Where(ragdocument.DocumentKeyEQ(upload.DocumentKey)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
	}

	versions, err := tx.RagDocumentVersion.
		Query().
		Where(ragdocumentversion.DocumentIDEQ(document.ID)).
		All(ctx)
	if err != nil {
		return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
	}
	if task, decision, _ := decideSubmission(document, versions, upload.ContentHash); decision != ingestion.SubmissionCreate {
		if err := tx.Commit(); err != nil {
			return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
		}
		committed = true
		return task, decision, nil
	}

	nextVersion := document.CurrentVersion
	for _, version := range versions {
		if version.Version > nextVersion {
			nextVersion = version.Version
		}
	}
	nextVersion++

	version, err := tx.RagDocumentVersion.
		Create().
		SetIngestionID(uuid.New()).
		SetDocumentID(document.ID).
		SetVersion(nextVersion).
		SetFileName(upload.FileName).
		SetContentHash(upload.ContentHash).
		SetOriginalMarkdown(string(upload.Markdown)).
		SetSourceBytes(upload.SourceBytes).
		SetStatus(string(domain.StatusQueued)).
		SetStage(string(domain.StageQueued)).
		SetChunkCount(0).
		SetEmbeddedChunkCount(0).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, ingestion.SubmissionCreate, documentStoreError(err)
	}
	committed = true
	return taskFromVersion(document.DocumentKey, version), ingestion.SubmissionCreate, nil
}

func (repository *IngestionRepository) GetTask(
	ctx context.Context,
	ingestionID uuid.UUID,
) (domain.Task, error) {
	version, err := repository.versionByIngestionID(ctx, ingestionID)
	if err != nil {
		return domain.Task{}, err
	}
	document, err := repository.client.RagDocument.Get(ctx, version.DocumentID)
	if ent.IsNotFound(err) {
		return domain.Task{}, ingestionNotFoundError()
	}
	if err != nil {
		return domain.Task{}, documentStoreError(err)
	}
	return taskFromVersion(document.DocumentKey, version), nil
}

func (repository *IngestionRepository) LoadSource(
	ctx context.Context,
	ingestionID uuid.UUID,
) (domain.Upload, error) {
	version, err := repository.versionByIngestionID(ctx, ingestionID)
	if err != nil {
		return domain.Upload{}, err
	}
	document, err := repository.client.RagDocument.Get(ctx, version.DocumentID)
	if ent.IsNotFound(err) {
		return domain.Upload{}, ingestionNotFoundError()
	}
	if err != nil {
		return domain.Upload{}, documentStoreError(err)
	}
	return domain.Upload{
		DocumentKey: document.DocumentKey,
		FileName:    version.FileName,
		Markdown:    []byte(version.OriginalMarkdown),
		ContentHash: version.ContentHash,
		SourceBytes: version.SourceBytes,
	}, nil
}

func (repository *IngestionRepository) UpdateStage(
	ctx context.Context,
	ingestionID uuid.UUID,
	status domain.Status,
	stage domain.Stage,
) error {
	sourceStatus, sourceStage, ok := allowedStageTransition(status, stage)
	if !ok {
		return invalidIngestionStateError()
	}
	affected, err := repository.client.RagDocumentVersion.
		Update().
		Where(
			ragdocumentversion.IngestionIDEQ(ingestionID),
			ragdocumentversion.StatusEQ(string(sourceStatus)),
			ragdocumentversion.StageEQ(string(sourceStage)),
		).
		SetStatus(string(status)).
		SetStage(string(stage)).
		SetUpdatedAt(repository.now()).
		Save(ctx)
	return repository.mapConditionalUpdateResult(ctx, ingestionID, affected, err)
}

func (repository *IngestionRepository) UpdateProgress(
	ctx context.Context,
	ingestionID uuid.UUID,
	chunkCount int,
	embeddedChunkCount int,
) error {
	affected, err := repository.client.RagDocumentVersion.
		Update().
		Where(
			ragdocumentversion.IngestionIDEQ(ingestionID),
			ragdocumentversion.StatusEQ(string(domain.StatusRunning)),
		).
		SetChunkCount(chunkCount).
		SetEmbeddedChunkCount(embeddedChunkCount).
		SetUpdatedAt(repository.now()).
		Save(ctx)
	return repository.mapConditionalUpdateResult(ctx, ingestionID, affected, err)
}

func (repository *IngestionRepository) MarkFailed(
	ctx context.Context,
	ingestionID uuid.UUID,
	stage domain.Stage,
	failure domain.Failure,
) error {
	now := repository.now()
	affected, err := repository.client.RagDocumentVersion.
		Update().
		Where(
			ragdocumentversion.IngestionIDEQ(ingestionID),
			ragdocumentversion.StatusIn(
				string(domain.StatusQueued),
				string(domain.StatusRunning),
			),
		).
		SetStatus(string(domain.StatusFailed)).
		SetStage(string(stage)).
		SetFailureCode(failure.Code).
		SetFailureMessage(failure.Message).
		SetUpdatedAt(now).
		SetCompletedAt(now).
		Save(ctx)
	return repository.mapConditionalUpdateResult(ctx, ingestionID, affected, err)
}

func (repository *IngestionRepository) StoreAndActivate(
	ctx context.Context,
	ingestionID uuid.UUID,
	chunks []domain.EmbeddedChunk,
) error {
	if len(chunks) == 0 {
		return invalidEmbeddingResponseError("不能激活空 Chunk 集合")
	}
	for _, chunk := range chunks {
		if len(chunk.Vector) != embeddingDimensions {
			return invalidEmbeddingResponseError("向量维度必须是 1024")
		}
		for _, value := range chunk.Vector {
			if math.IsNaN(value) ||
				math.IsInf(value, 0) ||
				math.Abs(value) > math.MaxFloat32 {
				return invalidEmbeddingResponseError("向量包含无法存储的数值")
			}
		}
	}

	tx, err := repository.client.Tx(ctx)
	if err != nil {
		return documentStoreError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	version, err := tx.RagDocumentVersion.
		Query().
		Where(ragdocumentversion.IngestionIDEQ(ingestionID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return ingestionNotFoundError()
	}
	if err != nil {
		return documentStoreError(err)
	}

	document, err := tx.RagDocument.
		Query().
		Where(ragdocument.IDEQ(version.DocumentID)).
		ForUpdate().
		Only(ctx)
	if ent.IsNotFound(err) {
		return ingestionNotFoundError()
	}
	if err != nil {
		return documentStoreError(err)
	}

	versions, err := tx.RagDocumentVersion.
		Query().
		Where(ragdocumentversion.DocumentIDEQ(document.ID)).
		ForUpdate().
		All(ctx)
	if err != nil {
		return documentStoreError(err)
	}
	version = findVersionByID(versions, version.ID)
	if version == nil {
		return ingestionNotFoundError()
	}
	if version.DocumentID != document.ID ||
		version.Status != string(domain.StatusRunning) ||
		version.Stage != string(domain.StageStoring) ||
		document.CurrentVersion >= version.Version ||
		hasSupersedingVersion(versions, version) {
		return invalidIngestionStateError()
	}

	now := repository.now()
	for _, chunk := range chunks {
		vector := make([]float32, len(chunk.Vector))
		for index, value := range chunk.Vector {
			vector[index] = float32(value)
		}
		if _, err := tx.RagChunk.
			Create().
			SetDocumentVersionID(version.ID).
			SetChunkIndex(chunk.ChunkIndex).
			SetContent(chunk.Content).
			SetEmbeddingContent(chunk.EmbeddingContent).
			SetHeadingPath(chunk.HeadingPath).
			SetStartLine(chunk.StartLine).
			SetEndLine(chunk.EndLine).
			SetEstimatedTokens(chunk.EstimatedTokens).
			SetContentHash(chunk.ContentHash).
			SetEmbedding(pgvector.NewVector(vector)).
			SetCreatedAt(now).
			Save(ctx); err != nil {
			return documentStoreError(err)
		}
	}

	count := len(chunks)
	affected, err := tx.RagDocumentVersion.
		Update().
		Where(
			ragdocumentversion.IDEQ(version.ID),
			ragdocumentversion.StatusEQ(string(domain.StatusRunning)),
			ragdocumentversion.StageEQ(string(domain.StageStoring)),
		).
		SetStatus(string(domain.StatusSucceeded)).
		SetStage(string(domain.StageCompleted)).
		SetChunkCount(count).
		SetEmbeddedChunkCount(count).
		SetUpdatedAt(now).
		SetCompletedAt(now).
		Save(ctx)
	if err != nil {
		return documentStoreError(err)
	}
	if affected != 1 {
		return documentStoreError(errors.New("激活版本更新数量异常"))
	}
	affected, err = tx.RagDocument.
		Update().
		Where(
			ragdocument.IDEQ(version.DocumentID),
			ragdocument.CurrentVersionLT(version.Version),
		).
		SetCurrentVersion(version.Version).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return documentStoreError(err)
	}
	if affected != 1 {
		return documentStoreError(errors.New("当前版本更新数量异常"))
	}
	if _, err := tx.RagDocumentVersion.
		Delete().
		Where(
			ragdocumentversion.DocumentIDEQ(version.DocumentID),
			ragdocumentversion.IDNEQ(version.ID),
		).
		Exec(ctx); err != nil {
		return documentStoreError(err)
	}
	if err := tx.Commit(); err != nil {
		return documentStoreError(err)
	}
	committed = true
	return nil
}

func (repository *IngestionRepository) versionByIngestionID(
	ctx context.Context,
	ingestionID uuid.UUID,
) (*ent.RagDocumentVersion, error) {
	version, err := repository.client.RagDocumentVersion.
		Query().
		Where(ragdocumentversion.IngestionIDEQ(ingestionID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ingestionNotFoundError()
	}
	if err != nil {
		return nil, documentStoreError(err)
	}
	return version, nil
}

func decideSubmission(
	document *ent.RagDocument,
	versions []*ent.RagDocumentVersion,
	contentHash string,
) (domain.Task, ingestion.SubmissionDecision, error) {
	for _, version := range versions {
		if version.Version == document.CurrentVersion && version.ContentHash == contentHash {
			return taskFromVersion(document.DocumentKey, version), ingestion.SubmissionReuse, nil
		}
	}
	for _, version := range versions {
		if isInProgress(version) && version.ContentHash == contentHash {
			return taskFromVersion(document.DocumentKey, version), ingestion.SubmissionReuse, nil
		}
	}
	for _, version := range versions {
		if isInProgress(version) {
			return taskFromVersion(document.DocumentKey, version), ingestion.SubmissionConflict, nil
		}
	}
	return domain.Task{}, ingestion.SubmissionCreate, nil
}

func isInProgress(version *ent.RagDocumentVersion) bool {
	return version.Status == string(domain.StatusQueued) ||
		version.Status == string(domain.StatusRunning)
}

func taskFromVersion(documentKey string, version *ent.RagDocumentVersion) domain.Task {
	task := domain.Task{
		IngestionID:        version.IngestionID,
		DocumentKey:        documentKey,
		Status:             domain.Status(version.Status),
		Stage:              domain.Stage(version.Stage),
		SourceBytes:        version.SourceBytes,
		ChunkCount:         version.ChunkCount,
		EmbeddedChunkCount: version.EmbeddedChunkCount,
		CreatedAt:          version.CreatedAt,
		UpdatedAt:          version.UpdatedAt,
		CompletedAt:        version.CompletedAt,
	}
	if version.FailureCode != nil || version.FailureMessage != nil {
		task.Failure = &domain.Failure{}
		if version.FailureCode != nil {
			task.Failure.Code = *version.FailureCode
		}
		if version.FailureMessage != nil {
			task.Failure.Message = *version.FailureMessage
		}
	}
	return task
}

func (repository *IngestionRepository) mapConditionalUpdateResult(
	ctx context.Context,
	ingestionID uuid.UUID,
	affected int,
	err error,
) error {
	if err != nil {
		return documentStoreError(err)
	}
	if affected > 0 {
		return nil
	}
	exists, err := repository.client.RagDocumentVersion.
		Query().
		Where(ragdocumentversion.IngestionIDEQ(ingestionID)).
		Exist(ctx)
	if err != nil {
		return documentStoreError(err)
	}
	if !exists {
		return ingestionNotFoundError()
	}
	return invalidIngestionStateError()
}

func allowedStageTransition(
	status domain.Status,
	stage domain.Stage,
) (domain.Status, domain.Stage, bool) {
	if status != domain.StatusRunning {
		return "", "", false
	}
	switch stage {
	case domain.StageParsing:
		return domain.StatusQueued, domain.StageQueued, true
	case domain.StageFiltering:
		return domain.StatusRunning, domain.StageParsing, true
	case domain.StageNormalizing:
		return domain.StatusRunning, domain.StageFiltering, true
	case domain.StageChunking:
		return domain.StatusRunning, domain.StageNormalizing, true
	case domain.StageEmbedding:
		return domain.StatusRunning, domain.StageChunking, true
	case domain.StageStoring:
		return domain.StatusRunning, domain.StageEmbedding, true
	default:
		return "", "", false
	}
}

func findVersionByID(
	versions []*ent.RagDocumentVersion,
	id int64,
) *ent.RagDocumentVersion {
	for _, version := range versions {
		if version.ID == id {
			return version
		}
	}
	return nil
}

func hasSupersedingVersion(
	versions []*ent.RagDocumentVersion,
	target *ent.RagDocumentVersion,
) bool {
	for _, version := range versions {
		if version.ID == target.ID {
			continue
		}
		if version.Version > target.Version || isInProgress(version) {
			return true
		}
	}
	return false
}

func documentStoreError(err error) error {
	return domain.NewError(domain.CodeDocumentStoreFailed, "文档存储失败", err)
}

func ingestionNotFoundError() error {
	return domain.NewError(domain.CodeIngestionNotFound, "导入任务不存在", nil)
}

func invalidIngestionStateError() error {
	return domain.NewError(
		domain.CodeInvalidIngestionState,
		"导入任务当前状态不允许此操作",
		nil,
	)
}

func invalidEmbeddingResponseError(message string) error {
	return domain.NewError(domain.CodeInvalidEmbeddingResponse, message, nil)
}

var _ ingestion.Repository = (*IngestionRepository)(nil)

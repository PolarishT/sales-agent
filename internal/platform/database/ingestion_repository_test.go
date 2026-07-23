package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/PolarishT/sales-agent/ent"
	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/PolarishT/sales-agent/internal/rag/ingestion"
	"github.com/google/uuid"
)

var (
	documentColumns = []string{
		"id", "document_key", "current_version", "created_at", "updated_at",
	}
	versionColumns = []string{
		"id", "ingestion_id", "document_id", "version", "file_name",
		"content_hash", "original_markdown", "source_bytes", "status", "stage",
		"chunk_count", "embedded_chunk_count", "failure_code", "failure_message",
		"created_at", "updated_at", "completed_at",
	}
)

type repositoryFixture struct {
	repository *IngestionRepository
	mock       sqlmock.Sqlmock
	close      func()
	now        time.Time
}

type vectorDimensionArgument int

func (dimension vectorDimensionArgument) Match(value driver.Value) bool {
	vector, ok := value.(string)
	if !ok || !strings.HasPrefix(vector, "[") || !strings.HasSuffix(vector, "]") {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(vector, "["), "]")
	if body == "" {
		return dimension == 0
	}
	return strings.Count(body, ",")+1 == int(dimension)
}

func newRepositoryFixture(t *testing.T) repositoryFixture {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(driver))
	repository, err := NewIngestionRepository(client)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC)
	repository.now = func() time.Time { return now }
	return repositoryFixture{
		repository: repository,
		mock:       mock,
		now:        now,
		close: func() {
			_ = client.Close()
			_ = db.Close()
		},
	}
}

func (f repositoryFixture) finish(t *testing.T) {
	t.Helper()
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	f.close()
}

func documentRow(id int64, key string, currentVersion int, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows(documentColumns).
		AddRow(id, key, currentVersion, now, now)
}

func versionRow(
	id int64,
	ingestionID uuid.UUID,
	documentID int64,
	version int,
	hash string,
	status domain.Status,
	stage domain.Stage,
	now time.Time,
) *sqlmock.Rows {
	return sqlmock.NewRows(versionColumns).AddRow(
		id,
		ingestionID.String(),
		documentID,
		version,
		"catalog.md",
		hash,
		"# catalog",
		int64(9),
		string(status),
		string(stage),
		0,
		0,
		nil,
		nil,
		now,
		now,
		nil,
	)
}

func versionRows() *sqlmock.Rows {
	return sqlmock.NewRows(versionColumns)
}

func expectDocumentQuery(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(`SELECT .* FROM "rag_documents".*"document_key".*\$1.*LIMIT 2`).
		WillReturnRows(rows)
}

func expectVersionQuery(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(`SELECT .* FROM "rag_document_versions"`).
		WillReturnRows(rows)
}

func TestIngestionInspectSubmissionReturnsCreateForMissingDocument(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	expectDocumentQuery(fixture.mock, sqlmock.NewRows(documentColumns))

	task, decision, err := fixture.repository.InspectSubmission(
		context.Background(),
		"catalog/global",
		"new-hash",
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision != ingestion.SubmissionCreate {
		t.Fatalf("decision = %v, want create", decision)
	}
	if task != (domain.Task{}) {
		t.Fatalf("task = %+v, want zero task", task)
	}
}

func TestIngestionInspectSubmissionReusesCurrentBeforeRunningConflict(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	currentID := uuid.New()
	expectDocumentQuery(fixture.mock, documentRow(10, "catalog/global", 4, fixture.now))
	expectVersionQuery(
		fixture.mock,
		versionRow(
			40,
			currentID,
			10,
			4,
			"same-hash",
			domain.StatusSucceeded,
			domain.StageCompleted,
			fixture.now,
		).AddRow(
			41,
			uuid.New().String(),
			10,
			5,
			"catalog.md",
			"other-hash",
			"# other",
			int64(7),
			string(domain.StatusRunning),
			string(domain.StageEmbedding),
			0,
			0,
			nil,
			nil,
			fixture.now,
			fixture.now,
			nil,
		),
	)

	task, decision, err := fixture.repository.InspectSubmission(
		context.Background(),
		"catalog/global",
		"same-hash",
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision != ingestion.SubmissionReuse {
		t.Fatalf("decision = %v, want reuse", decision)
	}
	if task.IngestionID != currentID || task.DocumentKey != "catalog/global" {
		t.Fatalf("task = %+v", task)
	}
}

func TestIngestionInspectSubmissionReusesMatchingRunningVersion(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	runningID := uuid.New()
	expectDocumentQuery(fixture.mock, documentRow(10, "catalog/global", 0, fixture.now))
	expectVersionQuery(
		fixture.mock,
		versionRow(
			41,
			runningID,
			10,
			1,
			"same-hash",
			domain.StatusQueued,
			domain.StageQueued,
			fixture.now,
		),
	)

	task, decision, err := fixture.repository.InspectSubmission(
		context.Background(),
		"catalog/global",
		"same-hash",
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision != ingestion.SubmissionReuse || task.IngestionID != runningID {
		t.Fatalf("decision, task = %v, %+v", decision, task)
	}
}

func TestIngestionInspectSubmissionConflictsWithDifferentRunningVersion(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	runningID := uuid.New()
	expectDocumentQuery(fixture.mock, documentRow(10, "catalog/global", 0, fixture.now))
	expectVersionQuery(
		fixture.mock,
		versionRow(
			41,
			runningID,
			10,
			1,
			"old-hash",
			domain.StatusRunning,
			domain.StageEmbedding,
			fixture.now,
		),
	)

	task, decision, err := fixture.repository.InspectSubmission(
		context.Background(),
		"catalog/global",
		"new-hash",
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision != ingestion.SubmissionConflict || task.IngestionID != runningID {
		t.Fatalf("decision, task = %v, %+v", decision, task)
	}
}

func TestIngestionCreateQueuedLocksAndUsesHighestVersion(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	fixture.mock.ExpectBegin()
	fixture.mock.ExpectQuery(`INSERT INTO "rag_documents".*ON CONFLICT.*"document_key".*DO UPDATE.*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(10))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*"document_key".*\$1.*LIMIT 2 FOR UPDATE`).
		WillReturnRows(documentRow(10, "catalog/global", 4, fixture.now))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions"`).
		WillReturnRows(
			versionRow(
				70,
				uuid.New(),
				10,
				7,
				"old-hash",
				domain.StatusSucceeded,
				domain.StageCompleted,
				fixture.now,
			),
		)
	fixture.mock.ExpectQuery(`INSERT INTO "rag_document_versions".*RETURNING "id"`).
		WithArgs(
			sqlmock.AnyArg(),
			8,
			"catalog.md",
			"new-hash",
			"# catalog",
			int64(9),
			string(domain.StatusQueued),
			string(domain.StageQueued),
			0,
			0,
			fixture.now,
			fixture.now,
			int64(10),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(80))
	fixture.mock.ExpectCommit()

	task, decision, err := fixture.repository.CreateQueued(context.Background(), domain.Upload{
		DocumentKey: "catalog/global",
		FileName:    "catalog.md",
		Markdown:    []byte("# catalog"),
		ContentHash: "new-hash",
		SourceBytes: 9,
	})
	if err != nil {
		t.Fatalf("CreateQueued() error = %v (cause: %v)", err, errors.Unwrap(err))
	}
	if decision != ingestion.SubmissionCreate {
		t.Fatalf("decision = %v, want create", decision)
	}
	if task.Status != domain.StatusQueued ||
		task.Stage != domain.StageQueued ||
		task.DocumentKey != "catalog/global" ||
		task.IngestionID == uuid.Nil {
		t.Fatalf("task = %+v", task)
	}
}

func TestIngestionCreateQueuedRollsBackOnVersionInsertFailure(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	fixture.mock.ExpectBegin()
	fixture.mock.ExpectQuery(`INSERT INTO "rag_documents"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(10))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*FOR UPDATE`).
		WillReturnRows(documentRow(10, "catalog/global", 0, fixture.now))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions"`).
		WillReturnRows(versionRows())
	fixture.mock.ExpectQuery(`INSERT INTO "rag_document_versions"`).
		WillReturnError(errors.New("insert failed"))
	fixture.mock.ExpectRollback()

	_, _, err := fixture.repository.CreateQueued(context.Background(), domain.Upload{
		DocumentKey: "catalog/global",
		FileName:    "catalog.md",
		Markdown:    []byte("# catalog"),
		ContentHash: "new-hash",
		SourceBytes: 9,
	})
	if !domain.IsCode(err, domain.CodeDocumentStoreFailed) {
		t.Fatalf("error = %v, want %s", err, domain.CodeDocumentStoreFailed)
	}
}

func TestIngestionCreateQueuedRechecksAndReusesCurrentBeforeConflict(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	currentID := uuid.New()
	fixture.mock.ExpectBegin()
	fixture.mock.ExpectQuery(`INSERT INTO "rag_documents".*ON CONFLICT.*"document_key".*DO UPDATE.*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(10))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*"document_key".*\$1.*LIMIT 2 FOR UPDATE`).
		WillReturnRows(documentRow(10, "catalog/global", 4, fixture.now))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions"`).
		WillReturnRows(
			versionRow(
				40,
				currentID,
				10,
				4,
				"same-hash",
				domain.StatusSucceeded,
				domain.StageCompleted,
				fixture.now,
			).AddRow(
				41,
				uuid.New().String(),
				10,
				5,
				"catalog.md",
				"other-hash",
				"# other",
				int64(7),
				string(domain.StatusRunning),
				string(domain.StageEmbedding),
				0,
				0,
				nil,
				nil,
				fixture.now,
				fixture.now,
				nil,
			),
		)
	fixture.mock.ExpectCommit()

	task, decision, err := fixture.repository.CreateQueued(context.Background(), domain.Upload{
		DocumentKey: "catalog/global",
		FileName:    "catalog.md",
		Markdown:    []byte("# catalog"),
		ContentHash: "same-hash",
		SourceBytes: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision != ingestion.SubmissionReuse || task.IngestionID != currentID {
		t.Fatalf("decision, task = %v, %+v", decision, task)
	}
}

func TestIngestionCreateQueuedMapsCommitFailure(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	fixture.mock.ExpectBegin()
	fixture.mock.ExpectQuery(`INSERT INTO "rag_documents"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(10))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*FOR UPDATE`).
		WillReturnRows(documentRow(10, "catalog/global", 0, fixture.now))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions"`).
		WillReturnRows(versionRows())
	fixture.mock.ExpectQuery(`INSERT INTO "rag_document_versions"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(80))
	fixture.mock.ExpectCommit().WillReturnError(errors.New("commit failed"))

	_, _, err := fixture.repository.CreateQueued(context.Background(), domain.Upload{
		DocumentKey: "catalog/global",
		FileName:    "catalog.md",
		Markdown:    []byte("# catalog"),
		ContentHash: "new-hash",
		SourceBytes: 9,
	})
	if !domain.IsCode(err, domain.CodeDocumentStoreFailed) {
		t.Fatalf("error = %v, want %s", err, domain.CodeDocumentStoreFailed)
	}
}

func TestIngestionGetTaskMapsMissingToStableNotFound(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	expectVersionQuery(fixture.mock, versionRows())

	_, err := fixture.repository.GetTask(context.Background(), uuid.New())
	if !domain.IsCode(err, domain.CodeIngestionNotFound) {
		t.Fatalf("error = %v, want %s", err, domain.CodeIngestionNotFound)
	}
}

func TestIngestionLoadSourceReturnsStoredMarkdown(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	ingestionID := uuid.New()
	expectVersionQuery(
		fixture.mock,
		versionRow(
			80,
			ingestionID,
			10,
			8,
			"content-hash",
			domain.StatusRunning,
			domain.StageParsing,
			fixture.now,
		),
	)
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*"id" = \$1.*LIMIT 2`).
		WithArgs(int64(10)).
		WillReturnRows(documentRow(10, "catalog/global", 7, fixture.now))

	upload, err := fixture.repository.LoadSource(context.Background(), ingestionID)
	if err != nil {
		t.Fatal(err)
	}
	if upload.DocumentKey != "catalog/global" ||
		upload.FileName != "catalog.md" ||
		string(upload.Markdown) != "# catalog" ||
		upload.ContentHash != "content-hash" ||
		upload.SourceBytes != 9 {
		t.Fatalf("upload = %+v", upload)
	}
}

func TestIngestionUpdateStageAndProgressPersistExactValues(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	ingestionID := uuid.New()
	fixture.mock.ExpectExec(`UPDATE "rag_document_versions" SET .*"status".*"stage".*"updated_at".*WHERE .*"ingestion_id" = \$4.*"status" = \$5.*"stage" = \$6`).
		WithArgs(
			string(domain.StatusRunning),
			string(domain.StageChunking),
			fixture.now,
			ingestionID,
			string(domain.StatusRunning),
			string(domain.StageNormalizing),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	fixture.mock.ExpectExec(`UPDATE "rag_document_versions" SET .*"chunk_count".*"embedded_chunk_count".*"updated_at".*WHERE .*"ingestion_id" = \$4.*"status" = \$5`).
		WithArgs(5, 3, fixture.now, ingestionID, string(domain.StatusRunning)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := fixture.repository.UpdateStage(
		context.Background(),
		ingestionID,
		domain.StatusRunning,
		domain.StageChunking,
	); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.UpdateProgress(
		context.Background(),
		ingestionID,
		5,
		3,
	); err != nil {
		t.Fatal(err)
	}
}

func TestIngestionConditionalUpdatesRejectTerminalTask(t *testing.T) {
	tests := []struct {
		name   string
		expect func(sqlmock.Sqlmock, uuid.UUID, time.Time)
		call   func(*IngestionRepository, uuid.UUID) error
	}{
		{
			name: "stage",
			expect: func(mock sqlmock.Sqlmock, ingestionID uuid.UUID, now time.Time) {
				mock.ExpectExec(`UPDATE "rag_document_versions".*WHERE .*"status" = \$5.*"stage" = \$6`).
					WillReturnResult(sqlmock.NewResult(0, 0))
			},
			call: func(repository *IngestionRepository, ingestionID uuid.UUID) error {
				return repository.UpdateStage(
					context.Background(),
					ingestionID,
					domain.StatusRunning,
					domain.StageChunking,
				)
			},
		},
		{
			name: "progress",
			expect: func(mock sqlmock.Sqlmock, ingestionID uuid.UUID, now time.Time) {
				mock.ExpectExec(`UPDATE "rag_document_versions".*WHERE .*"status" = \$5`).
					WillReturnResult(sqlmock.NewResult(0, 0))
			},
			call: func(repository *IngestionRepository, ingestionID uuid.UUID) error {
				return repository.UpdateProgress(context.Background(), ingestionID, 5, 3)
			},
		},
		{
			name: "failure",
			expect: func(mock sqlmock.Sqlmock, ingestionID uuid.UUID, now time.Time) {
				mock.ExpectExec(`UPDATE "rag_document_versions".*WHERE .*"status" IN \(\$8, \$9\)`).
					WillReturnResult(sqlmock.NewResult(0, 0))
			},
			call: func(repository *IngestionRepository, ingestionID uuid.UUID) error {
				return repository.MarkFailed(
					context.Background(),
					ingestionID,
					domain.StageEmbedding,
					domain.Failure{Code: domain.CodeEmbeddingFailed, Message: "向量生成失败"},
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			defer fixture.finish(t)
			ingestionID := uuid.New()
			test.expect(fixture.mock, ingestionID, fixture.now)
			fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"ingestion_id" = \$1 LIMIT 1`).
				WithArgs(ingestionID).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(80))

			err := test.call(fixture.repository, ingestionID)
			if !domain.IsCode(err, domain.CodeInvalidIngestionState) {
				t.Fatalf(
					"error = %v (cause: %v), want %s",
					err,
					errors.Unwrap(err),
					domain.CodeInvalidIngestionState,
				)
			}
		})
	}
}

func TestIngestionConditionalUpdateDistinguishesMissingTask(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	ingestionID := uuid.New()
	fixture.mock.ExpectExec(`UPDATE "rag_document_versions".*WHERE .*"status" = \$5`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"ingestion_id" = \$1 LIMIT 1`).
		WithArgs(ingestionID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	err := fixture.repository.UpdateProgress(context.Background(), ingestionID, 5, 3)
	if !domain.IsCode(err, domain.CodeIngestionNotFound) {
		t.Fatalf(
			"error = %v (cause: %v), want %s",
			err,
			errors.Unwrap(err),
			domain.CodeIngestionNotFound,
		)
	}
}

func TestIngestionUpdateStageRejectsIllegalTransitionBeforeSQL(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)

	err := fixture.repository.UpdateStage(
		context.Background(),
		uuid.New(),
		domain.StatusSucceeded,
		domain.StageCompleted,
	)
	if !domain.IsCode(err, domain.CodeInvalidIngestionState) {
		t.Fatalf("error = %v, want %s", err, domain.CodeInvalidIngestionState)
	}
}

func TestIngestionMarkFailedStoresOnlyStableFailure(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	ingestionID := uuid.New()
	fixture.mock.ExpectExec(`UPDATE "rag_document_versions" SET .*"status".*"stage".*"failure_code".*"failure_message".*"updated_at".*"completed_at".*WHERE .*"ingestion_id" = \$7.*"status" IN \(\$8, \$9\)`).
		WithArgs(
			string(domain.StatusFailed),
			string(domain.StageEmbedding),
			domain.CodeEmbeddingFailed,
			"向量生成失败",
			fixture.now,
			fixture.now,
			ingestionID,
			string(domain.StatusQueued),
			string(domain.StatusRunning),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := fixture.repository.MarkFailed(
		context.Background(),
		ingestionID,
		domain.StageEmbedding,
		domain.Failure{Code: domain.CodeEmbeddingFailed, Message: "向量生成失败"},
	)
	if err != nil {
		t.Fatalf("MarkFailed() error = %v (cause: %v)", err, errors.Unwrap(err))
	}
}

func TestIngestionStoreAndActivateRejectsEmptyChunksBeforeTransaction(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)

	err := fixture.repository.StoreAndActivate(context.Background(), uuid.New(), nil)
	if !domain.IsCode(err, domain.CodeInvalidEmbeddingResponse) {
		t.Fatalf("error = %v, want %s", err, domain.CodeInvalidEmbeddingResponse)
	}
}

func TestIngestionStoreAndActivateRejectsWrongDimensionBeforeTransaction(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)

	err := fixture.repository.StoreAndActivate(
		context.Background(),
		uuid.New(),
		[]domain.EmbeddedChunk{{
			Chunk:  domain.Chunk{ChunkIndex: 0, Content: "x", EmbeddingContent: "x"},
			Vector: make([]float64, 1023),
		}},
	)
	if !domain.IsCode(err, domain.CodeInvalidEmbeddingResponse) {
		t.Fatalf("error = %v, want %s", err, domain.CodeInvalidEmbeddingResponse)
	}
}

func TestIngestionStoreAndActivateRejectsNonFiniteOrFloat32OverflowBeforeTransaction(t *testing.T) {
	tests := []struct {
		name  string
		value float64
	}{
		{name: "nan", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "float32 overflow", value: math.MaxFloat64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			defer fixture.finish(t)
			vector := make([]float64, 1024)
			vector[100] = test.value

			err := fixture.repository.StoreAndActivate(
				context.Background(),
				uuid.New(),
				[]domain.EmbeddedChunk{{
					Chunk:  domain.Chunk{ChunkIndex: 0, Content: "x", EmbeddingContent: "x"},
					Vector: vector,
				}},
			)
			if !domain.IsCode(err, domain.CodeInvalidEmbeddingResponse) {
				t.Fatalf("error = %v, want %s", err, domain.CodeInvalidEmbeddingResponse)
			}
		})
	}
}

func TestIngestionStoreAndActivateCommitsAtomicReplacement(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	ingestionID := uuid.New()
	fixture.mock.ExpectBegin()
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"ingestion_id".*\$1.*LIMIT 2`).
		WillReturnRows(
			versionRow(
				80,
				ingestionID,
				10,
				8,
				"new-hash",
				domain.StatusRunning,
				domain.StageStoring,
				fixture.now,
			),
		)
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*"id" = \$1.*LIMIT 2 FOR UPDATE`).
		WithArgs(int64(10)).
		WillReturnRows(documentRow(10, "catalog/global", 7, fixture.now))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"document_id" = \$1.*FOR UPDATE`).
		WithArgs(int64(10)).
		WillReturnRows(
			versionRow(
				80,
				ingestionID,
				10,
				8,
				"new-hash",
				domain.StatusRunning,
				domain.StageStoring,
				fixture.now,
			),
		)
	fixture.mock.ExpectQuery(`INSERT INTO "rag_chunks".*RETURNING "id"`).
		WithArgs(
			0,
			"商品正文",
			"商品正文",
			sqlmock.AnyArg(),
			1,
			1,
			4,
			strings.Repeat("a", 64),
			vectorDimensionArgument(1024),
			fixture.now,
			int64(80),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(90))
	fixture.mock.ExpectExec(`UPDATE "rag_document_versions" SET .*"status".*"stage".*"chunk_count".*"embedded_chunk_count".*"updated_at".*"completed_at".*WHERE .*"id" = \$7.*"status" = \$8.*"stage" = \$9`).
		WithArgs(
			string(domain.StatusSucceeded),
			string(domain.StageCompleted),
			1,
			1,
			fixture.now,
			fixture.now,
			int64(80),
			string(domain.StatusRunning),
			string(domain.StageStoring),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	fixture.mock.ExpectExec(`UPDATE "rag_documents" SET .*"current_version".*"updated_at".*WHERE .*"id" = \$3.*"current_version" < \$4`).
		WithArgs(8, fixture.now, int64(10), 8).
		WillReturnResult(sqlmock.NewResult(0, 1))
	fixture.mock.ExpectExec(`DELETE FROM "rag_document_versions".*"document_id" = \$1.*"id" <> \$2`).
		WithArgs(int64(10), int64(80)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	fixture.mock.ExpectCommit()

	err := fixture.repository.StoreAndActivate(
		context.Background(),
		ingestionID,
		[]domain.EmbeddedChunk{{
			Chunk: domain.Chunk{
				ChunkIndex:       0,
				Content:          "商品正文",
				EmbeddingContent: "商品正文",
				HeadingPath:      []string{"商品"},
				StartLine:        1,
				EndLine:          1,
				EstimatedTokens:  4,
				ContentHash:      strings.Repeat("a", 64),
			},
			Vector: make([]float64, 1024),
		}},
	)
	if err != nil {
		t.Fatalf("StoreAndActivate() error = %v (cause: %v)", err, errors.Unwrap(err))
	}
}

func TestIngestionStoreAndActivateRejectsTerminalStaleOrSupersededTarget(t *testing.T) {
	tests := []struct {
		name           string
		currentVersion int
		targetVersion  int
		targetStatus   domain.Status
		targetStage    domain.Stage
	}{
		{
			name:           "terminal retry",
			currentVersion: 8,
			targetVersion:  8,
			targetStatus:   domain.StatusSucceeded,
			targetStage:    domain.StageCompleted,
		},
		{
			name:           "failed task",
			currentVersion: 7,
			targetVersion:  8,
			targetStatus:   domain.StatusFailed,
			targetStage:    domain.StageStoring,
		},
		{
			name:           "wrong stage",
			currentVersion: 7,
			targetVersion:  8,
			targetStatus:   domain.StatusRunning,
			targetStage:    domain.StageEmbedding,
		},
		{
			name:           "stale current version",
			currentVersion: 9,
			targetVersion:  8,
			targetStatus:   domain.StatusRunning,
			targetStage:    domain.StageStoring,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			defer fixture.finish(t)
			ingestionID := uuid.New()
			fixture.mock.ExpectBegin()
			fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"ingestion_id".*\$1.*LIMIT 2`).
				WillReturnRows(
					versionRow(
						80,
						ingestionID,
						10,
						test.targetVersion,
						"target-hash",
						test.targetStatus,
						test.targetStage,
						fixture.now,
					),
				)
			fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*"id" = \$1.*LIMIT 2 FOR UPDATE`).
				WillReturnRows(documentRow(10, "catalog/global", test.currentVersion, fixture.now))
			fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"document_id" = \$1.*FOR UPDATE`).
				WillReturnRows(
					versionRow(
						80,
						ingestionID,
						10,
						test.targetVersion,
						"target-hash",
						test.targetStatus,
						test.targetStage,
						fixture.now,
					),
				)
			fixture.mock.ExpectRollback()

			err := fixture.repository.StoreAndActivate(
				context.Background(),
				ingestionID,
				[]domain.EmbeddedChunk{{
					Chunk:  domain.Chunk{ChunkIndex: 0, Content: "x", EmbeddingContent: "x"},
					Vector: make([]float64, 1024),
				}},
			)
			if !domain.IsCode(err, domain.CodeInvalidIngestionState) {
				t.Fatalf("error = %v, want %s", err, domain.CodeInvalidIngestionState)
			}
		})
	}
}

func TestIngestionStoreAndActivateRejectsOtherActiveOrHigherVersion(t *testing.T) {
	tests := []struct {
		name        string
		otherID     uuid.UUID
		otherNumber int
		otherStatus domain.Status
	}{
		{
			name:        "other active version",
			otherID:     uuid.New(),
			otherNumber: 7,
			otherStatus: domain.StatusQueued,
		},
		{
			name:        "higher terminal version",
			otherID:     uuid.New(),
			otherNumber: 9,
			otherStatus: domain.StatusFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			defer fixture.finish(t)
			ingestionID := uuid.New()
			fixture.mock.ExpectBegin()
			fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"ingestion_id".*\$1.*LIMIT 2`).
				WillReturnRows(
					versionRow(
						80,
						ingestionID,
						10,
						8,
						"target-hash",
						domain.StatusRunning,
						domain.StageStoring,
						fixture.now,
					),
				)
			fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*"id" = \$1.*LIMIT 2 FOR UPDATE`).
				WillReturnRows(documentRow(10, "catalog/global", 6, fixture.now))
			fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"document_id" = \$1.*FOR UPDATE`).
				WillReturnRows(
					versionRow(
						80,
						ingestionID,
						10,
						8,
						"target-hash",
						domain.StatusRunning,
						domain.StageStoring,
						fixture.now,
					).AddRow(
						81,
						test.otherID.String(),
						10,
						test.otherNumber,
						"catalog.md",
						"other-hash",
						"# other",
						int64(7),
						string(test.otherStatus),
						string(domain.StageQueued),
						0,
						0,
						nil,
						nil,
						fixture.now,
						fixture.now,
						nil,
					),
				)
			fixture.mock.ExpectRollback()

			err := fixture.repository.StoreAndActivate(
				context.Background(),
				ingestionID,
				[]domain.EmbeddedChunk{{
					Chunk:  domain.Chunk{ChunkIndex: 0, Content: "x", EmbeddingContent: "x"},
					Vector: make([]float64, 1024),
				}},
			)
			if !domain.IsCode(err, domain.CodeInvalidIngestionState) {
				t.Fatalf("error = %v, want %s", err, domain.CodeInvalidIngestionState)
			}
		})
	}
}

func TestIngestionStoreAndActivateRollsBackOnChunkInsertFailure(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	ingestionID := uuid.New()
	fixture.mock.ExpectBegin()
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"ingestion_id"`).
		WillReturnRows(
			versionRow(
				80,
				ingestionID,
				10,
				8,
				"new-hash",
				domain.StatusRunning,
				domain.StageStoring,
				fixture.now,
			),
		)
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*FOR UPDATE`).
		WillReturnRows(documentRow(10, "catalog/global", 7, fixture.now))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"document_id".*FOR UPDATE`).
		WillReturnRows(
			versionRow(
				80,
				ingestionID,
				10,
				8,
				"new-hash",
				domain.StatusRunning,
				domain.StageStoring,
				fixture.now,
			),
		)
	fixture.mock.ExpectQuery(`INSERT INTO "rag_chunks"`).
		WillReturnError(errors.New("chunk insert failed"))
	fixture.mock.ExpectRollback()

	err := fixture.repository.StoreAndActivate(
		context.Background(),
		ingestionID,
		[]domain.EmbeddedChunk{{
			Chunk: domain.Chunk{
				ChunkIndex:       0,
				Content:          "商品正文",
				EmbeddingContent: "商品正文",
				HeadingPath:      []string{"商品"},
				StartLine:        1,
				EndLine:          1,
				EstimatedTokens:  4,
				ContentHash:      strings.Repeat("a", 64),
			},
			Vector: make([]float64, 1024),
		}},
	)
	if !domain.IsCode(err, domain.CodeDocumentStoreFailed) {
		t.Fatalf("error = %v, want %s", err, domain.CodeDocumentStoreFailed)
	}
}

func TestIngestionStoreAndActivateRollsBackOnActivationUpdateFailure(t *testing.T) {
	fixture := newRepositoryFixture(t)
	defer fixture.finish(t)
	ingestionID := uuid.New()
	fixture.mock.ExpectBegin()
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"ingestion_id"`).
		WillReturnRows(
			versionRow(
				80,
				ingestionID,
				10,
				8,
				"new-hash",
				domain.StatusRunning,
				domain.StageStoring,
				fixture.now,
			),
		)
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_documents".*FOR UPDATE`).
		WillReturnRows(documentRow(10, "catalog/global", 7, fixture.now))
	fixture.mock.ExpectQuery(`SELECT .* FROM "rag_document_versions".*"document_id".*FOR UPDATE`).
		WillReturnRows(
			versionRow(
				80,
				ingestionID,
				10,
				8,
				"new-hash",
				domain.StatusRunning,
				domain.StageStoring,
				fixture.now,
			),
		)
	fixture.mock.ExpectQuery(`INSERT INTO "rag_chunks"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(90))
	fixture.mock.ExpectExec(`UPDATE "rag_document_versions"`).
		WillReturnError(errors.New("update failed"))
	fixture.mock.ExpectRollback()

	err := fixture.repository.StoreAndActivate(
		context.Background(),
		ingestionID,
		[]domain.EmbeddedChunk{{
			Chunk: domain.Chunk{
				ChunkIndex:       0,
				Content:          "商品正文",
				EmbeddingContent: "商品正文",
				HeadingPath:      []string{"商品"},
				StartLine:        1,
				EndLine:          1,
				EstimatedTokens:  4,
				ContentHash:      strings.Repeat("a", 64),
			},
			Vector: make([]float64, 1024),
		}},
	)
	if !domain.IsCode(err, domain.CodeDocumentStoreFailed) {
		t.Fatalf("error = %v, want %s", err, domain.CodeDocumentStoreFailed)
	}
}

func TestIngestionRepositoryRejectsNilClient(t *testing.T) {
	_, err := NewIngestionRepository(nil)
	if err == nil {
		t.Fatal("NewIngestionRepository(nil) error = nil")
	}
}

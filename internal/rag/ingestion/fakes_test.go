package ingestion

import (
	"context"
	"sync"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/google/uuid"
)

type fakeRepository struct {
	mu sync.Mutex

	inspectTask     domain.Task
	inspectDecision SubmissionDecision
	inspectErr      error
	createTask      domain.Task
	createDecision  SubmissionDecision
	createErr       error
	createEntered   chan struct{}
	createRelease   chan struct{}
	getTask         domain.Task
	getErr          error
	source          domain.Upload
	loadErr         error
	stageErr        map[domain.Stage]error
	progressErr     error
	activateErr     error
	markFailedErr   error
	markFailedPanic any

	inspectCalls int
	createCalls  int
	getCalls     int
	loadCalls    int
	events       []string
	progress     [][2]int
	failures     []failureRecord
	activated    []domain.EmbeddedChunk
}

type failureRecord struct {
	ingestionID uuid.UUID
	stage       domain.Stage
	failure     domain.Failure
}

func (f *fakeRepository) InspectSubmission(
	_ context.Context,
	_ string,
	_ string,
) (domain.Task, SubmissionDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspectCalls++
	return f.inspectTask, f.inspectDecision, f.inspectErr
}

func (f *fakeRepository) CreateQueued(
	_ context.Context,
	upload domain.Upload,
) (domain.Task, SubmissionDecision, error) {
	f.mu.Lock()
	f.createCalls++
	entered := f.createEntered
	release := f.createRelease
	task := f.createTask
	decision := f.createDecision
	err := f.createErr
	f.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	_ = upload
	return task, decision, err
}

func (f *fakeRepository) GetTask(_ context.Context, _ uuid.UUID) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	return f.getTask, f.getErr
}

func (f *fakeRepository) LoadSource(_ context.Context, _ uuid.UUID) (domain.Upload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadCalls++
	f.events = append(f.events, "load")
	return f.source, f.loadErr
}

func (f *fakeRepository) UpdateStage(
	_ context.Context,
	_ uuid.UUID,
	_ domain.Status,
	stage domain.Stage,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "stage:"+string(stage))
	return f.stageErr[stage]
}

func (f *fakeRepository) UpdateProgress(
	_ context.Context,
	_ uuid.UUID,
	chunks int,
	embedded int,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress = append(f.progress, [2]int{chunks, embedded})
	if embedded == 0 {
		f.events = append(f.events, "progress:chunks")
	} else {
		f.events = append(f.events, "progress:embedded")
	}
	return f.progressErr
}

func (f *fakeRepository) MarkFailed(
	_ context.Context,
	ingestionID uuid.UUID,
	stage domain.Stage,
	failure domain.Failure,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, failureRecord{
		ingestionID: ingestionID,
		stage:       stage,
		failure:     failure,
	})
	if f.markFailedPanic != nil {
		panic(f.markFailedPanic)
	}
	return f.markFailedErr
}

func (f *fakeRepository) StoreAndActivate(
	_ context.Context,
	_ uuid.UUID,
	chunks []domain.EmbeddedChunk,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "activate")
	f.activated = append([]domain.EmbeddedChunk(nil), chunks...)
	return f.activateErr
}

func (f *fakeRepository) snapshot() (events []string, failures []failureRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...), append([]failureRecord(nil), f.failures...)
}

type fakeParser struct {
	repository *fakeRepository
	document   domain.ParsedDocument
	err        error
}

func (f *fakeParser) Parse(context.Context, []byte) (domain.ParsedDocument, error) {
	f.repository.mu.Lock()
	f.repository.events = append(f.repository.events, "parse")
	f.repository.mu.Unlock()
	return f.document, f.err
}

type fakeFilter struct {
	repository *fakeRepository
	document   domain.ParsedDocument
	err        error
}

func (f *fakeFilter) Apply(context.Context, domain.ParsedDocument) (domain.ParsedDocument, error) {
	f.repository.mu.Lock()
	f.repository.events = append(f.repository.events, "filter")
	f.repository.mu.Unlock()
	return f.document, f.err
}

type fakeNormalizer struct {
	repository *fakeRepository
	document   domain.NormalizedDocument
	err        error
}

func (f *fakeNormalizer) Normalize(
	context.Context,
	domain.ParsedDocument,
) (domain.NormalizedDocument, error) {
	f.repository.mu.Lock()
	f.repository.events = append(f.repository.events, "normalize")
	f.repository.mu.Unlock()
	return f.document, f.err
}

type fakeSplitter struct {
	repository *fakeRepository
	chunks     []domain.Chunk
	err        error
}

func (f *fakeSplitter) Split(
	context.Context,
	domain.NormalizedDocument,
	domain.ChunkConfig,
) ([]domain.Chunk, error) {
	f.repository.mu.Lock()
	f.repository.events = append(f.repository.events, "split")
	f.repository.mu.Unlock()
	return f.chunks, f.err
}

type fakeEmbedder struct {
	repository *fakeRepository
	vectors    [][]float64
	err        error
	inputs     []string
}

func (f *fakeEmbedder) EmbedStrings(
	_ context.Context,
	texts []string,
	_ ...embedding.Option,
) ([][]float64, error) {
	f.repository.mu.Lock()
	f.repository.events = append(f.repository.events, "embed")
	f.repository.mu.Unlock()
	f.inputs = append([]string(nil), texts...)
	return f.vectors, f.err
}

package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/google/uuid"
)

func TestServiceChecksReuseBeforeReserving(t *testing.T) {
	task := domain.Task{IngestionID: uuid.New(), Status: domain.StatusSucceeded}
	repository := &fakeRepository{
		inspectTask:     task,
		inspectDecision: SubmissionReuse,
	}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("failed to fill executor capacity")
	}
	defer reservation.Release()
	service := mustService(t, repository, executor)

	submission, err := service.Submit(context.Background(), "catalog/item", "item.md", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	if !submission.Deduplicated || submission.Task.IngestionID != task.IngestionID {
		t.Fatalf("submission = %+v", submission)
	}
	if repository.createCalls != 0 {
		t.Fatalf("CreateQueued calls = %d", repository.createCalls)
	}
}

func TestServiceMapsInitialConflict(t *testing.T) {
	repository := &fakeRepository{inspectDecision: SubmissionConflict}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
	service := mustService(t, repository, executor)

	_, err := service.Submit(context.Background(), "catalog/item", "item.md", []byte("content"))
	if !domain.IsCode(err, domain.CodeIngestionInProgress) {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceReleasesReservationAfterCreateError(t *testing.T) {
	repository := &fakeRepository{
		inspectDecision: SubmissionCreate,
		createErr:       errors.New("private database detail"),
	}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
	service := mustService(t, repository, executor)

	if _, err := service.Submit(context.Background(), "catalog/item", "item.md", []byte("content")); !domain.IsCode(err, domain.CodeIngestionUnavailable) {
		t.Fatalf("Submit error = %v", err)
	}
	if reservation, ok := executor.TryReserve(); !ok {
		t.Fatal("reservation capacity was not released")
	} else {
		reservation.Release()
	}
}

func TestServiceSanitizesInitialRepositoryFailure(t *testing.T) {
	repository := &fakeRepository{
		inspectErr: errors.New("private database detail"),
	}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
	service := mustService(t, repository, executor)

	_, err := service.Submit(context.Background(), "catalog/item", "item.md", []byte("content"))
	if !domain.IsCode(err, domain.CodeIngestionUnavailable) ||
		strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("Submit error = %v", err)
	}
}

func TestServiceReleasesReservationAfterTransactionalReuseOrConflict(t *testing.T) {
	for _, decision := range []SubmissionDecision{SubmissionReuse, SubmissionConflict} {
		t.Run(decisionName(decision), func(t *testing.T) {
			task := domain.Task{IngestionID: uuid.New(), Status: domain.StatusSucceeded}
			repository := &fakeRepository{
				inspectDecision: SubmissionCreate,
				createTask:      task,
				createDecision:  decision,
			}
			executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
			service := mustService(t, repository, executor)

			submission, err := service.Submit(context.Background(), "catalog/item", "item.md", []byte("content"))
			if decision == SubmissionConflict {
				if !domain.IsCode(err, domain.CodeIngestionInProgress) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || !submission.Deduplicated {
				t.Fatalf("submission = %+v, error = %v", submission, err)
			}
			if reservation, ok := executor.TryReserve(); !ok {
				t.Fatal("reservation capacity was not released")
			} else {
				reservation.Release()
			}
		})
	}
}

func TestServiceCommitsOnlyIngestionID(t *testing.T) {
	ingestionID := uuid.New()
	repository := &fakeRepository{
		inspectDecision: SubmissionCreate,
		createDecision:  SubmissionCreate,
		createTask: domain.Task{
			IngestionID: ingestionID,
			Status:      domain.StatusQueued,
		},
	}
	runner := &recordingRunner{called: make(chan uuid.UUID, 1)}
	executor := mustStartedExecutor(t, 1, 0, runner, repository)
	service := mustService(t, repository, executor)

	submission, err := service.Submit(context.Background(), " catalog/item ", "item.md", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	if submission.Deduplicated || submission.Task.IngestionID != ingestionID {
		t.Fatalf("submission = %+v", submission)
	}
	select {
	case got := <-runner.called:
		if got != ingestionID {
			t.Fatalf("runner ID = %s, want %s", got, ingestionID)
		}
	case <-time.After(time.Second):
		t.Fatal("runner was not called")
	}
}

func TestServiceDoesNotReportSuccessWhenShutdownAbortsBeforeCommit(t *testing.T) {
	ingestionID := uuid.New()
	createEntered := make(chan struct{})
	createRelease := make(chan struct{})
	repository := &fakeRepository{
		inspectDecision: SubmissionCreate,
		createDecision:  SubmissionCreate,
		createTask: domain.Task{
			IngestionID: ingestionID,
			Status:      domain.StatusQueued,
			Stage:       domain.StageQueued,
		},
		createEntered: createEntered,
		createRelease: createRelease,
		getTask:       domain.Task{IngestionID: ingestionID, Stage: domain.StageQueued},
	}
	runner := &recordingRunner{called: make(chan uuid.UUID, 1)}
	executor := mustStartedExecutor(t, 1, 0, runner, repository)
	service := mustService(t, repository, executor)

	type result struct {
		submission domain.Submission
		err        error
	}
	resultChannel := make(chan result, 1)
	go func() {
		submission, err := service.Submit(
			context.Background(),
			"catalog/item",
			"item.md",
			[]byte("content"),
		)
		resultChannel <- result{submission: submission, err: err}
	}()
	<-createEntered

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := executor.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v", err)
	}
	close(createRelease)

	resultValue := <-resultChannel
	if !domain.IsCode(resultValue.err, domain.CodeIngestionUnavailable) {
		t.Fatalf("Submit() = %+v, %v", resultValue.submission, resultValue.err)
	}
	select {
	case id := <-runner.called:
		t.Fatalf("aborted reservation ran task %s", id)
	default:
	}
	_, failures := repository.snapshot()
	if len(failures) != 1 ||
		failures[0].ingestionID != ingestionID ||
		failures[0].stage != domain.StageQueued ||
		failures[0].failure.Code != domain.CodeProcessInterrupted {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestServiceQueueFullDoesNotCreateTask(t *testing.T) {
	repository := &fakeRepository{inspectDecision: SubmissionCreate}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("failed to fill executor")
	}
	defer reservation.Release()
	service := mustService(t, repository, executor)

	_, err := service.Submit(context.Background(), "catalog/item", "item.md", []byte("content"))
	if !domain.IsCode(err, domain.CodeIngestionQueueFull) {
		t.Fatalf("error = %v", err)
	}
	if repository.createCalls != 0 {
		t.Fatalf("CreateQueued calls = %d", repository.createCalls)
	}
}

func TestServiceGetDelegatesToRepository(t *testing.T) {
	id := uuid.New()
	want := domain.Task{IngestionID: id}
	repository := &fakeRepository{getTask: want}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
	service := mustService(t, repository, executor)

	got, err := service.Get(context.Background(), id)
	if err != nil || got.IngestionID != want.IngestionID {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
}

func TestServiceGetPreservesNotFoundAndSanitizesOtherRepositoryErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{
			name: "not found",
			err:  domain.NewError(domain.CodeIngestionNotFound, "导入任务不存在", nil),
			code: domain.CodeIngestionNotFound,
		},
		{
			name: "store",
			err:  domain.NewError(domain.CodeDocumentStoreFailed, "文档存储失败", errors.New("private")),
			code: domain.CodeIngestionUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{getErr: test.err}
			executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
			service := mustService(t, repository, executor)
			_, err := service.Get(context.Background(), uuid.New())
			if !domain.IsCode(err, test.code) {
				t.Fatalf("Get error = %v, want %s", err, test.code)
			}
		})
	}
}

func mustService(t *testing.T, repository Repository, executor *Executor) *Service {
	t.Helper()
	service, err := NewService(repository, executor, 5<<20)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func decisionName(decision SubmissionDecision) string {
	if decision == SubmissionReuse {
		return "reuse"
	}
	return "conflict"
}

package ingestion

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/google/uuid"
)

func TestQueuedReuseRecoversOnNewExecutorAfterCompensationFailure(t *testing.T) {
	tests := []struct {
		name      string
		markError error
		markPanic any
	}{
		{name: "error", markError: errors.New("temporary mark failure")},
		{name: "panic", markPanic: "temporary mark panic"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
				createEntered:   createEntered,
				createRelease:   createRelease,
				getTask:         domain.Task{IngestionID: ingestionID, Stage: domain.StageQueued},
				markFailedErr:   test.markError,
				markFailedPanic: test.markPanic,
			}
			oldExecutor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, repository)
			oldService := mustService(t, repository, oldExecutor)

			submitResult := make(chan error, 1)
			go func() {
				_, err := oldService.Submit(
					context.Background(),
					"catalog/item",
					"item.md",
					[]byte("content"),
				)
				submitResult <- err
			}()
			<-createEntered
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if err := oldExecutor.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Shutdown() error = %v", err)
			}
			cancel()
			close(createRelease)
			if err := <-submitResult; !domain.IsCode(err, domain.CodeIngestionUnavailable) {
				t.Fatalf("first Submit error = %v", err)
			}

			repository.mu.Lock()
			repository.inspectTask = repository.createTask
			repository.inspectDecision = SubmissionReuse
			repository.inspectErr = nil
			repository.createEntered = nil
			repository.createRelease = nil
			repository.mu.Unlock()

			called := make(chan uuid.UUID, 1)
			newExecutor := mustStartedExecutor(t, 1, 0, &recordingRunner{called: called}, repository)
			newService := mustService(t, repository, newExecutor)
			submission, err := newService.Submit(
				context.Background(),
				"catalog/item",
				"item.md",
				[]byte("content"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !submission.Deduplicated || submission.Task.IngestionID != ingestionID {
				t.Fatalf("submission = %+v", submission)
			}
			select {
			case got := <-called:
				if got != ingestionID {
					t.Fatalf("runner ID = %s, want %s", got, ingestionID)
				}
			case <-time.After(time.Second):
				t.Fatal("stranded QUEUED task was not rescheduled")
			}
		})
	}
}

func TestQueuedReuseReturnsQueueFullThenRecoversOnSameExecutor(t *testing.T) {
	ingestionID := uuid.New()
	repository := &fakeRepository{
		inspectTask: domain.Task{
			IngestionID: ingestionID,
			Status:      domain.StatusQueued,
			Stage:       domain.StageQueued,
		},
		inspectDecision: SubmissionReuse,
	}
	called := make(chan uuid.UUID, 1)
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{called: called}, repository)
	service := mustService(t, repository, executor)
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("failed to occupy executor capacity")
	}

	if _, err := service.Submit(
		context.Background(),
		"catalog/item",
		"item.md",
		[]byte("content"),
	); !domain.IsCode(err, domain.CodeIngestionQueueFull) {
		t.Fatalf("full Submit error = %v", err)
	}
	reservation.Release()

	submission, err := service.Submit(
		context.Background(),
		"catalog/item",
		"item.md",
		[]byte("content"),
	)
	if err != nil || !submission.Deduplicated {
		t.Fatalf("recovery Submit = %+v, %v", submission, err)
	}
	select {
	case got := <-called:
		if got != ingestionID {
			t.Fatalf("runner ID = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("QUEUED task was not scheduled after capacity recovered")
	}
}

func TestConcurrentQueuedReuseSchedulesExactlyOnce(t *testing.T) {
	ingestionID := uuid.New()
	repository := &fakeRepository{
		inspectTask: domain.Task{
			IngestionID: ingestionID,
			Status:      domain.StatusQueued,
			Stage:       domain.StageQueued,
		},
		inspectDecision: SubmissionReuse,
	}
	runnerStarted := make(chan struct{})
	runnerRelease := make(chan struct{})
	var runnerCalls atomic.Int32
	executor := mustStartedExecutor(t, 1, 8, &recordingRunner{
		run: func(context.Context, uuid.UUID) error {
			if runnerCalls.Add(1) == 1 {
				close(runnerStarted)
			}
			<-runnerRelease
			return nil
		},
	}, repository)
	service := mustService(t, repository, executor)

	const submissions = 32
	start := make(chan struct{})
	var wait sync.WaitGroup
	errorsSeen := make(chan error, submissions)
	wait.Add(submissions)
	for index := 0; index < submissions; index++ {
		go func() {
			defer wait.Done()
			<-start
			submission, err := service.Submit(
				context.Background(),
				"catalog/item",
				"item.md",
				[]byte("content"),
			)
			if err == nil && (!submission.Deduplicated || submission.Task.IngestionID != ingestionID) {
				err = errors.New("unexpected submission")
			}
			errorsSeen <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Submit error = %v", err)
		}
	}
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("QUEUED task was not scheduled")
	}
	if got := runnerCalls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want 1", got)
	}
	close(runnerRelease)
	shutdownExecutor(t, executor)
}

func TestQueuedReuseCannotScheduleWhileExecutorIsStopping(t *testing.T) {
	repository := &fakeRepository{
		inspectTask: domain.Task{
			IngestionID: uuid.New(),
			Status:      domain.StatusQueued,
			Stage:       domain.StageQueued,
		},
		inspectDecision: SubmissionReuse,
	}
	executor := mustStartedExecutor(t, 1, 1, &recordingRunner{}, repository)
	service := mustService(t, repository, executor)
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}

	shutdownResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownResult <- executor.Shutdown(ctx)
	}()
	deadline := time.Now().Add(time.Second)
	for executor.accepting() {
		if time.Now().After(deadline) {
			t.Fatal("executor did not enter stopping state")
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := service.Submit(
		context.Background(),
		"catalog/item",
		"item.md",
		[]byte("content"),
	); !domain.IsCode(err, domain.CodeIngestionUnavailable) {
		t.Fatalf("stopping Submit error = %v", err)
	}
	reservation.Release()
	if err := <-shutdownResult; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTransactionalQueuedReuseSchedulesExistingID(t *testing.T) {
	ingestionID := uuid.New()
	repository := &fakeRepository{
		inspectDecision: SubmissionCreate,
		createDecision:  SubmissionReuse,
		createTask: domain.Task{
			IngestionID: ingestionID,
			Status:      domain.StatusQueued,
			Stage:       domain.StageQueued,
		},
	}
	called := make(chan uuid.UUID, 1)
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{called: called}, repository)
	service := mustService(t, repository, executor)

	submission, err := service.Submit(
		context.Background(),
		"catalog/item",
		"item.md",
		[]byte("content"),
	)
	if err != nil || !submission.Deduplicated {
		t.Fatalf("Submit = %+v, %v", submission, err)
	}
	select {
	case got := <-called:
		if got != ingestionID {
			t.Fatalf("runner ID = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("transactional QUEUED reuse was not scheduled")
	}
}

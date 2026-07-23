package ingestion

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/google/uuid"
)

func TestRunningReuseRequiresCurrentExecutorSchedule(t *testing.T) {
	t.Run("scheduled in current executor", func(t *testing.T) {
		ingestionID := uuid.New()
		repository := &fakeRepository{
			inspectTask: domain.Task{
				IngestionID: ingestionID,
				Status:      domain.StatusRunning,
				Stage:       domain.StageEmbedding,
			},
			inspectDecision: SubmissionReuse,
		}
		started := make(chan struct{})
		release := make(chan struct{})
		var calls atomic.Int32
		executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
			run: func(context.Context, uuid.UUID) error {
				calls.Add(1)
				close(started)
				<-release
				return nil
			},
		}, repository)
		reservation, ok := executor.TryReserve()
		if !ok {
			t.Fatal("reservation rejected")
		}
		if !reservation.Commit(ingestionID) {
			t.Fatal("commit rejected")
		}
		<-started
		if !executor.IsScheduled(ingestionID) {
			t.Fatal("running task is not marked scheduled")
		}

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
		if got := calls.Load(); got != 1 {
			t.Fatalf("runner calls = %d", got)
		}
		close(release)
		shutdownExecutor(t, executor)
	})

	t.Run("stale in new executor", func(t *testing.T) {
		ingestionID := uuid.New()
		repository := &fakeRepository{
			inspectTask: domain.Task{
				IngestionID: ingestionID,
				Status:      domain.StatusRunning,
				Stage:       domain.StageEmbedding,
			},
			inspectDecision: SubmissionReuse,
		}
		called := make(chan uuid.UUID, 1)
		executor := mustStartedExecutor(t, 1, 0, &recordingRunner{called: called}, repository)
		service := mustService(t, repository, executor)

		_, err := service.Submit(
			context.Background(),
			"catalog/item",
			"item.md",
			[]byte("content"),
		)
		if !domain.IsCode(err, domain.CodeIngestionUnavailable) {
			t.Fatalf("Submit error = %v", err)
		}
		select {
		case id := <-called:
			t.Fatalf("stale RUNNING task was re-executed: %s", id)
		default:
		}
	})
}

func TestFailedPersistenceLeavesStaleRunningExplicitlyUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		markError error
		markPanic any
	}{
		{name: "error", markError: errors.New("mark failed")},
		{name: "panic", markPanic: "mark panic secret"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ingestionID := uuid.New()
			repository := &fakeRepository{
				inspectTask: domain.Task{
					IngestionID: ingestionID,
					Status:      domain.StatusRunning,
					Stage:       domain.StageEmbedding,
				},
				inspectDecision: SubmissionReuse,
				getTask: domain.Task{
					IngestionID: ingestionID,
					Status:      domain.StatusRunning,
					Stage:       domain.StageEmbedding,
				},
				markFailedErr:   test.markError,
				markFailedPanic: test.markPanic,
			}
			var calls atomic.Int32
			executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
				run: func(context.Context, uuid.UUID) error {
					calls.Add(1)
					return errors.New("runner failure")
				},
			}, repository)
			reservation, ok := executor.TryReserve()
			if !ok || !reservation.Commit(ingestionID) {
				t.Fatal("failed to schedule task")
			}
			waitUntilUnscheduled(t, executor, ingestionID)

			service := mustService(t, repository, executor)
			_, err := service.Submit(
				context.Background(),
				"catalog/item",
				"item.md",
				[]byte("content"),
			)
			if !domain.IsCode(err, domain.CodeIngestionUnavailable) {
				t.Fatalf("Submit error = %v", err)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("runner calls = %d, stale RUNNING was re-executed", got)
			}
		})
	}
}

func TestFailurePersistenceRetriesErrorsAndPanicsWithFreshDeadline(t *testing.T) {
	tests := []struct {
		name   string
		errors []error
		panics []any
	}{
		{
			name:   "errors",
			errors: []error{errors.New("first"), errors.New("second"), nil},
		},
		{
			name:   "panics",
			panics: []any{"first secret", "second secret", nil},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ingestionID := uuid.New()
			var deadlineCalls atomic.Int32
			repository := &fakeRepository{
				getTask: domain.Task{
					IngestionID: ingestionID,
					Status:      domain.StatusRunning,
					Stage:       domain.StageEmbedding,
				},
				markFailedErrs:   test.errors,
				markFailedPanics: test.panics,
				markFailedHook: func(ctx context.Context, _ int) error {
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) <= 0 {
						return errors.New("missing fresh persistence deadline")
					}
					deadlineCalls.Add(1)
					return nil
				},
			}
			executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
				run: func(context.Context, uuid.UUID) error {
					return errors.New("runner failure")
				},
			}, repository)
			reservation, ok := executor.TryReserve()
			if !ok || !reservation.Commit(ingestionID) {
				t.Fatal("failed to schedule task")
			}
			waitUntilUnscheduled(t, executor, ingestionID)

			repository.mu.Lock()
			calls := repository.markFailedCalls
			failures := len(repository.failures)
			repository.mu.Unlock()
			if calls != 3 || failures != 1 {
				t.Fatalf("MarkFailed calls/failures = %d/%d, want 3/1", calls, failures)
			}
			if deadlineCalls.Load() != 1 {
				t.Fatalf("successful deadline checks = %d, want 1", deadlineCalls.Load())
			}
		})
	}
}

func TestFailurePersistenceRetriesAreBounded(t *testing.T) {
	tests := []struct {
		name      string
		markError error
		markPanic any
	}{
		{name: "error", markError: errors.New("always fails")},
		{name: "panic", markPanic: "always panics"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ingestionID := uuid.New()
			repository := &fakeRepository{
				getTask:         domain.Task{IngestionID: ingestionID, Stage: domain.StageEmbedding},
				markFailedErr:   test.markError,
				markFailedPanic: test.markPanic,
			}
			executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
				run: func(context.Context, uuid.UUID) error {
					return errors.New("runner failure")
				},
			}, repository)
			reservation, ok := executor.TryReserve()
			if !ok || !reservation.Commit(ingestionID) {
				t.Fatal("failed to schedule task")
			}
			waitUntilUnscheduled(t, executor, ingestionID)
			repository.mu.Lock()
			calls := repository.markFailedCalls
			repository.mu.Unlock()
			if calls != 3 {
				t.Fatalf("MarkFailed calls = %d, want 3", calls)
			}
		})
	}
}

func TestShutdownDeadlineCancelsPersistenceAndWaitsForWorkerExit(t *testing.T) {
	ingestionID := uuid.New()
	repository := &fakeRepository{
		getTask: domain.Task{
			IngestionID: ingestionID,
			Status:      domain.StatusRunning,
			Stage:       domain.StageEmbedding,
		},
		markFailedHook: func(ctx context.Context, _ int) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	runnerStarted := make(chan struct{})
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
		run: func(ctx context.Context, _ uuid.UUID) error {
			close(runnerStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}, repository)
	reservation, ok := executor.TryReserve()
	if !ok || !reservation.Commit(ingestionID) {
		t.Fatal("failed to schedule task")
	}
	<-runnerStarted

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	if err := executor.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("Shutdown exceeded bounded cancellation time: %s", elapsed)
	}
	select {
	case <-executor.workerDone:
	default:
		t.Fatal("Shutdown returned before worker goroutines exited")
	}
	if executor.IsScheduled(ingestionID) {
		t.Fatal("Shutdown returned with a scheduled task")
	}
	repository.mu.Lock()
	calls := repository.markFailedCalls
	repository.mu.Unlock()
	if calls != 1 {
		t.Fatalf("MarkFailed calls = %d, want one canceled best-effort attempt", calls)
	}
}

func waitUntilUnscheduled(
	t *testing.T,
	executor *Executor,
	ingestionID uuid.UUID,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for executor.IsScheduled(ingestionID) {
		if time.Now().After(deadline) {
			t.Fatal("task remained scheduled")
		}
		time.Sleep(time.Millisecond)
	}
}

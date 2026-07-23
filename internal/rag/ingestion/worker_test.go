package ingestion

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/google/uuid"
)

func TestWorkerAppliesConfiguredTaskTimeout(t *testing.T) {
	const timeout = 40 * time.Millisecond
	deadlineSeen := make(chan time.Duration, 1)
	runner := &recordingRunner{run: func(ctx context.Context, _ uuid.UUID) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("missing deadline")
		}
		deadlineSeen <- time.Until(deadline)
		<-ctx.Done()
		return ctx.Err()
	}}
	repository := &fakeRepository{getTask: domain.Task{Stage: domain.StageEmbedding}}
	executor, err := NewExecutor(1, 0, timeout, runner, repository)
	if err != nil {
		t.Fatal(err)
	}
	executor.Start(context.Background())
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	reservation.Commit(uuid.New())

	select {
	case remaining := <-deadlineSeen:
		if remaining <= 0 || remaining > timeout {
			t.Fatalf("task deadline remaining = %s", remaining)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not observe deadline")
	}
	shutdownExecutor(t, executor)
	_, failures := repository.snapshot()
	if len(failures) != 1 || failures[0].failure.Code != domain.CodeProcessInterrupted {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestWorkerTreatsSwallowedDeadlineAsInterrupted(t *testing.T) {
	repository := &fakeRepository{getTask: domain.Task{Stage: domain.StageEmbedding}}
	executor, err := NewExecutor(1, 0, 20*time.Millisecond, &recordingRunner{
		run: func(ctx context.Context, _ uuid.UUID) error {
			<-ctx.Done()
			return nil
		},
	}, repository)
	if err != nil {
		t.Fatal(err)
	}
	executor.Start(context.Background())
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	reservation.Commit(uuid.New())
	shutdownExecutor(t, executor)

	_, failures := repository.snapshot()
	if len(failures) != 1 || failures[0].failure.Code != domain.CodeProcessInterrupted {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestWorkerRecoversPanicWithoutLeakingValueAndContinues(t *testing.T) {
	const secret = "panic-secret-markdown"
	var calls atomic.Int32
	secondRan := make(chan struct{})
	runner := &recordingRunner{run: func(context.Context, uuid.UUID) error {
		if calls.Add(1) == 1 {
			panic(secret)
		}
		close(secondRan)
		return nil
	}}
	repository := &fakeRepository{getTask: domain.Task{Stage: domain.StageParsing}}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	executor := mustStartedExecutor(t, 1, 1, runner, repository)
	for index := 0; index < 2; index++ {
		reservation, ok := executor.TryReserve()
		if !ok {
			t.Fatalf("reservation %d rejected", index)
		}
		reservation.Commit(uuid.New())
	}
	select {
	case <-secondRan:
	case <-time.After(time.Second):
		t.Fatal("worker did not continue after panic")
	}
	shutdownExecutor(t, executor)

	_, failures := repository.snapshot()
	if len(failures) != 1 || failures[0].failure.Code != domain.CodeInternalProcessing {
		t.Fatalf("failures = %#v", failures)
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("panic value leaked to logs: %s", logs.String())
	}
}

func TestWorkerPreservesStableFailureAndCurrentStage(t *testing.T) {
	stable := domain.NewError(domain.CodeDocumentSplitFailed, "文档切分失败", errors.New("private"))
	repository := &fakeRepository{getTask: domain.Task{
		DocumentKey: "catalog/item",
		Stage:       domain.StageChunking,
	}}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
		run: func(context.Context, uuid.UUID) error { return stable },
	}, repository)
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	reservation.Commit(uuid.New())
	shutdownExecutor(t, executor)

	_, failures := repository.snapshot()
	if len(failures) != 1 ||
		failures[0].stage != domain.StageChunking ||
		failures[0].failure.Code != domain.CodeDocumentSplitFailed ||
		failures[0].failure.Message != "文档切分失败" {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestWorkerPreservesStableEmbeddingTimeoutWhenTaskContextIsActive(t *testing.T) {
	repository := &fakeRepository{getTask: domain.Task{Stage: domain.StageEmbedding}}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
		run: func(context.Context, uuid.UUID) error {
			return domain.NewError(
				domain.CodeEmbeddingFailed,
				"文本向量生成失败",
				context.DeadlineExceeded,
			)
		},
	}, repository)
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	reservation.Commit(uuid.New())
	shutdownExecutor(t, executor)

	_, failures := repository.snapshot()
	if len(failures) != 1 || failures[0].failure.Code != domain.CodeEmbeddingFailed {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestWorkerUsesPipelineStageWhenTaskReloadFails(t *testing.T) {
	repository := &fakeRepository{getErr: errors.New("transient read failure")}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
		run: func(context.Context, uuid.UUID) error {
			return atStage(
				domain.StageEmbedding,
				domain.NewError(domain.CodeEmbeddingFailed, "文本向量生成失败", nil),
			)
		},
	}, repository)
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	reservation.Commit(uuid.New())
	shutdownExecutor(t, executor)

	_, failures := repository.snapshot()
	if len(failures) != 1 || failures[0].stage != domain.StageEmbedding {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestWorkerSanitizesNonProcessingDomainError(t *testing.T) {
	repository := &fakeRepository{getTask: domain.Task{Stage: domain.StageParsing}}
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{
		run: func(context.Context, uuid.UUID) error {
			return domain.NewError(
				domain.CodeInvalidDocumentKey,
				"supplier-controlled message",
				nil,
			)
		},
	}, repository)
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	reservation.Commit(uuid.New())
	shutdownExecutor(t, executor)

	_, failures := repository.snapshot()
	if len(failures) != 1 ||
		failures[0].failure.Code != domain.CodeInternalProcessing ||
		failures[0].failure.Message != "文档导入处理失败" {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestMarkFailedStateRaceDoesNotStopNextJob(t *testing.T) {
	var calls atomic.Int32
	secondRan := make(chan struct{})
	repository := &fakeRepository{
		getTask: domain.Task{Stage: domain.StageStoring},
		markFailedErr: domain.NewError(
			domain.CodeInvalidIngestionState,
			"任务已进入终态",
			nil,
		),
	}
	executor := mustStartedExecutor(t, 1, 1, &recordingRunner{
		run: func(context.Context, uuid.UUID) error {
			if calls.Add(1) == 1 {
				return errors.New("late failure")
			}
			close(secondRan)
			return nil
		},
	}, repository)
	for index := 0; index < 2; index++ {
		reservation, ok := executor.TryReserve()
		if !ok {
			t.Fatalf("reservation %d rejected", index)
		}
		reservation.Commit(uuid.New())
	}
	select {
	case <-secondRan:
	case <-time.After(time.Second):
		t.Fatal("worker stopped after MarkFailed state race")
	}
	shutdownExecutor(t, executor)
}

func TestMarkFailedPanicDoesNotStopNextJobOrLeakValue(t *testing.T) {
	const secret = "mark-failed-panic-secret"
	var calls atomic.Int32
	secondRan := make(chan struct{})
	repository := &fakeRepository{
		getTask:         domain.Task{Stage: domain.StageStoring},
		markFailedPanic: secret,
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	executor := mustStartedExecutor(t, 1, 1, &recordingRunner{
		run: func(context.Context, uuid.UUID) error {
			if calls.Add(1) == 1 {
				return errors.New("first failed")
			}
			close(secondRan)
			return nil
		},
	}, repository)
	for index := 0; index < 2; index++ {
		reservation, ok := executor.TryReserve()
		if !ok {
			t.Fatalf("reservation %d rejected", index)
		}
		reservation.Commit(uuid.New())
	}
	select {
	case <-secondRan:
	case <-time.After(time.Second):
		t.Fatal("worker stopped after MarkFailed panic")
	}
	shutdownExecutor(t, executor)
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("panic value leaked to logs: %s", logs.String())
	}
}

func TestShutdownCancelsQueuedJobsWithoutRunningThem(t *testing.T) {
	firstStarted := make(chan struct{})
	var calls atomic.Int32
	runner := &recordingRunner{run: func(ctx context.Context, _ uuid.UUID) error {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	repository := &fakeRepository{getTask: domain.Task{Stage: domain.StageQueued}}
	executor := mustStartedExecutor(t, 1, 1, runner, repository)
	for index := 0; index < 2; index++ {
		reservation, ok := executor.TryReserve()
		if !ok {
			t.Fatalf("reservation %d rejected", index)
		}
		reservation.Commit(uuid.New())
	}
	<-firstStarted

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := executor.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-executor.workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker goroutine did not exit after cancellation")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, queued task ran after cancellation", got)
	}
	_, failures := repository.snapshot()
	if len(failures) != 2 {
		t.Fatalf("failure count = %d, want 2", len(failures))
	}
	for _, failure := range failures {
		if failure.failure.Code != domain.CodeProcessInterrupted {
			t.Fatalf("failure = %#v", failure)
		}
	}
}

func TestShutdownReturnsAtDeadlineAndCancelsRunningTask(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	runner := &recordingRunner{run: func(ctx context.Context, _ uuid.UUID) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}}
	executor := mustStartedExecutor(t, 1, 0, runner, &fakeRepository{})
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	reservation.Commit(uuid.New())
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := executor.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if time.Since(start) < 15*time.Millisecond {
		t.Fatalf("Shutdown returned before deadline: %s", time.Since(start))
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("running task was not canceled")
	}
	select {
	case <-executor.workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker goroutine did not exit")
	}
	if reservation, ok := executor.TryReserve(); ok {
		reservation.Release()
		t.Fatal("executor accepted after shutdown")
	}
}

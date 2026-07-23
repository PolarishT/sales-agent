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

type recordingRunner struct {
	called chan uuid.UUID
	run    func(context.Context, uuid.UUID) error
}

func (r *recordingRunner) Run(ctx context.Context, id uuid.UUID) error {
	if r.called != nil {
		r.called <- id
	}
	if r.run != nil {
		return r.run(ctx, id)
	}
	return nil
}

func TestExecutorAcceptsOneRunningAndEightWaitingReservations(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &recordingRunner{run: func(context.Context, uuid.UUID) error {
		close(started)
		<-release
		return nil
	}}
	repository := &fakeRepository{}
	executor := mustStartedExecutor(t, 1, 8, runner, repository)

	first, ok := executor.TryReserve()
	if !ok {
		t.Fatal("running reservation rejected")
	}
	first.Commit(uuid.New())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}

	waiters := make([]*Reservation, 0, 8)
	for index := 0; index < 8; index++ {
		reservation, accepted := executor.TryReserve()
		if !accepted {
			t.Fatalf("waiting reservation %d rejected", index)
		}
		waiters = append(waiters, reservation)
	}
	if reservation, accepted := executor.TryReserve(); accepted {
		reservation.Release()
		t.Fatal("executor accepted reservation beyond total capacity")
	}

	for _, reservation := range waiters {
		reservation.Release()
	}
	close(release)
	shutdownExecutor(t, executor)
}

func TestReservationReleaseRestoresCapacityAndTerminalActionIsExactlyOnce(t *testing.T) {
	repository := &fakeRepository{}
	runner := &recordingRunner{called: make(chan uuid.UUID, 1)}
	executor := mustStartedExecutor(t, 1, 0, runner, repository)

	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	var terminal sync.WaitGroup
	terminal.Add(2)
	go func() {
		defer terminal.Done()
		reservation.Release()
	}()
	go func() {
		defer terminal.Done()
		reservation.Commit(uuid.New())
	}()
	terminal.Wait()

	deadline := time.Now().Add(time.Second)
	for {
		next, accepted := executor.TryReserve()
		if accepted {
			next.Release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal action did not restore capacity")
		}
		time.Sleep(time.Millisecond)
	}
	shutdownExecutor(t, executor)
}

func TestOneWorkerNeverRunsJobsConcurrently(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	runner := &recordingRunner{run: func(context.Context, uuid.UUID) error {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return nil
	}}
	executor := mustStartedExecutor(t, 1, 1, runner, &fakeRepository{})
	for index := 0; index < 2; index++ {
		reservation, ok := executor.TryReserve()
		if !ok {
			t.Fatalf("reservation %d rejected", index)
		}
		reservation.Commit(uuid.New())
	}

	<-started
	select {
	case <-started:
		t.Fatal("second job started before first completed")
	case <-time.After(25 * time.Millisecond):
	}
	release <- struct{}{}
	<-started
	release <- struct{}{}
	shutdownExecutor(t, executor)
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrency = %d", got)
	}
}

func TestReservationObtainedBeforeShutdownRemainsCommittable(t *testing.T) {
	called := make(chan uuid.UUID, 1)
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{called: called}, &fakeRepository{})
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
	select {
	case err := <-shutdownResult:
		t.Fatalf("Shutdown returned before reservation terminal action: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	id := uuid.New()
	reservation.Commit(id)
	select {
	case got := <-called:
		if got != id {
			t.Fatalf("runner ID = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-shutdown reservation was not executed")
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestShutdownDeadlineCancelsUncommittedReservation(t *testing.T) {
	executor := mustStartedExecutor(t, 1, 0, &recordingRunner{}, &fakeRepository{})
	reservation, ok := executor.TryReserve()
	if !ok {
		t.Fatal("reservation rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := executor.Shutdown(ctx); err != context.DeadlineExceeded {
		t.Fatalf("Shutdown() error = %v", err)
	}
	reservation.Commit(uuid.New())
}

func TestShutdownDeadlinePersistsInterruptedForRunningAndQueuedJobs(t *testing.T) {
	repository := &contextRespectingFailureRepository{
		fakeRepository: &fakeRepository{getTask: domain.Task{Stage: domain.StageEmbedding}},
	}
	runnerStarted := make(chan struct{})
	executor := mustStartedExecutor(t, 1, 1, &recordingRunner{
		run: func(ctx context.Context, _ uuid.UUID) error {
			close(runnerStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}, repository)

	for index := 0; index < 2; index++ {
		reservation, ok := executor.TryReserve()
		if !ok || !reservation.Commit(uuid.New()) {
			t.Fatalf("failed to schedule job %d", index)
		}
	}
	<-runnerStarted

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := executor.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v", err)
	}

	_, failures := repository.snapshot()
	if len(failures) != 2 {
		t.Fatalf("persisted failures = %#v, want running and queued failures", failures)
	}
	for _, failure := range failures {
		if failure.failure.Code != domain.CodeProcessInterrupted {
			t.Fatalf("failure = %#v, want %s", failure, domain.CodeProcessInterrupted)
		}
	}
}

func TestStartAndShutdownAreRaceSafeAndShutdownRejectsNewReservations(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		executor, err := NewExecutor(1, 1, time.Second, &recordingRunner{}, &fakeRepository{})
		if err != nil {
			t.Fatal(err)
		}
		var calls sync.WaitGroup
		calls.Add(2)
		go func() {
			defer calls.Done()
			executor.Start(context.Background())
		}()
		go func() {
			defer calls.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = executor.Shutdown(ctx)
		}()
		calls.Wait()
		if reservation, ok := executor.TryReserve(); ok {
			reservation.Release()
			t.Fatal("executor accepted after shutdown")
		}
	}
}

type contextRespectingFailureRepository struct {
	*fakeRepository
}

func (r *contextRespectingFailureRepository) GetTask(
	ctx context.Context,
	ingestionID uuid.UUID,
) (domain.Task, error) {
	if err := ctx.Err(); err != nil {
		return domain.Task{}, err
	}
	return r.fakeRepository.GetTask(ctx, ingestionID)
}

func (r *contextRespectingFailureRepository) MarkFailed(
	ctx context.Context,
	ingestionID uuid.UUID,
	stage domain.Stage,
	failure domain.Failure,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.fakeRepository.MarkFailed(ctx, ingestionID, stage, failure)
}

func mustStartedExecutor(
	t *testing.T,
	workers int,
	queueCapacity int,
	runner Runner,
	repository Repository,
) *Executor {
	t.Helper()
	executor, err := NewExecutor(workers, queueCapacity, time.Second, runner, repository)
	if err != nil {
		t.Fatal(err)
	}
	executor.Start(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = executor.Shutdown(ctx)
	})
	return executor
}

func shutdownExecutor(t *testing.T, executor *Executor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := executor.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

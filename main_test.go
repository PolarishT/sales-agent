package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenListenerReturnsErrorWhenAddressIsInUse(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用测试端口: %v", err)
	}
	defer occupied.Close()

	listener, err := openListener(occupied.Addr().String())
	if err == nil {
		listener.Close()
		t.Fatal("openListener() error = nil, want an error")
	}
}

type fakeRuntimeServer struct {
	runError    error
	runStarted  chan struct{}
	releaseRun  chan struct{}
	shutdown    *recordingShutdownComponent
	startedOnce sync.Once
}

func (f *fakeRuntimeServer) Run() error {
	f.startedOnce.Do(func() {
		if f.runStarted != nil {
			close(f.runStarted)
		}
	})
	if f.releaseRun != nil {
		<-f.releaseRun
	}
	return f.runError
}

func (f *fakeRuntimeServer) Shutdown(ctx context.Context) error {
	return f.shutdown.Shutdown(ctx)
}

type recordingShutdownComponent struct {
	name     string
	calls    *[]string
	contexts *[]context.Context
	err      error
}

func (f *recordingShutdownComponent) Shutdown(ctx context.Context) error {
	if f.calls != nil {
		*f.calls = append(*f.calls, f.name)
	}
	if f.contexts != nil {
		*f.contexts = append(*f.contexts, ctx)
	}
	return f.err
}

type recordingCloseComponent struct {
	name  string
	calls *[]string
	err   error
}

func (f *recordingCloseComponent) Close() error {
	if f.calls != nil {
		*f.calls = append(*f.calls, f.name)
	}
	return f.err
}

func TestShutdownRuntimeStopsHTTPBeforeWorkerWithOneDeadline(t *testing.T) {
	calls := make([]string, 0, 2)
	contexts := make([]context.Context, 0, 2)
	httpServer := &recordingShutdownComponent{
		name: "http", calls: &calls, contexts: &contexts,
	}
	worker := &recordingShutdownComponent{
		name: "worker", calls: &calls, contexts: &contexts,
	}

	if err := shutdownRuntime(httpServer, worker, time.Second); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(calls, ","); got != "http,worker" {
		t.Fatalf("shutdown order = %q, want http,worker", got)
	}
	if len(contexts) != 2 || contexts[0] != contexts[1] {
		t.Fatalf("shutdown contexts = %#v, want the same context", contexts)
	}
	firstDeadline, firstOK := contexts[0].Deadline()
	secondDeadline, secondOK := contexts[1].Deadline()
	if !firstOK || !secondOK || !firstDeadline.Equal(secondDeadline) {
		t.Fatalf("shutdown deadlines = %v/%t and %v/%t", firstDeadline, firstOK, secondDeadline, secondOK)
	}
}

func TestShutdownRuntimePreservesHTTPAndWorkerErrors(t *testing.T) {
	httpError := errors.New("http shutdown")
	workerError := errors.New("worker shutdown")
	httpServer := &recordingShutdownComponent{err: httpError}
	worker := &recordingShutdownComponent{err: workerError}

	err := shutdownRuntime(httpServer, worker, time.Second)

	if !errors.Is(err, httpError) {
		t.Fatalf("error = %v, want HTTP error", err)
	}
	if !errors.Is(err, workerError) {
		t.Fatalf("error = %v, want worker error", err)
	}
	if strings.Index(err.Error(), httpError.Error()) > strings.Index(err.Error(), workerError.Error()) {
		t.Fatalf("error precedence = %q, want HTTP before worker", err)
	}
}

func TestServeShutsDownWorkerAfterUnexpectedHTTPStop(t *testing.T) {
	runError := errors.New("http run")
	workerError := errors.New("worker shutdown")
	calls := make([]string, 0, 2)
	httpShutdown := &recordingShutdownComponent{name: "http", calls: &calls}
	worker := &recordingShutdownComponent{name: "worker", calls: &calls, err: workerError}
	httpServer := &fakeRuntimeServer{
		runError: runError,
		shutdown: httpShutdown,
	}
	database := &recordingCloseComponent{}

	err := serve(context.Background(), httpServer, worker, database, time.Second)

	if !errors.Is(err, runError) {
		t.Fatalf("serve() error = %v, want run error", err)
	}
	if !errors.Is(err, workerError) {
		t.Fatalf("serve() error = %v, want worker error", err)
	}
	if got := strings.Join(calls, ","); got != "http,worker" {
		t.Fatalf("shutdown order = %q, want http,worker", got)
	}
}

func TestServeUsesShutdownLifecycleAfterContextCancellation(t *testing.T) {
	calls := make([]string, 0, 2)
	runStarted := make(chan struct{})
	releaseRun := make(chan struct{})
	httpShutdown := &recordingShutdownComponent{name: "http", calls: &calls}
	worker := &recordingShutdownComponent{name: "worker", calls: &calls}
	httpServer := &fakeRuntimeServer{
		runStarted: runStarted,
		releaseRun: releaseRun,
		shutdown:   httpShutdown,
	}
	database := &recordingCloseComponent{}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serve(ctx, httpServer, worker, database, time.Second)
	}()
	<-runStarted
	cancel()

	if err := <-result; err != nil {
		t.Fatal(err)
	}
	close(releaseRun)
	if got := strings.Join(calls, ","); got != "http,worker" {
		t.Fatalf("shutdown order = %q, want http,worker", got)
	}
}

func TestStopRuntimeWaitsForDeadlineWorkerBeforeClosingDatabase(t *testing.T) {
	calls := make([]string, 0, 4)
	httpServer := &recordingShutdownComponent{name: "http", calls: &calls}
	worker := shutdownComponentFunc(func(ctx context.Context) error {
		calls = append(calls, "worker.cancel")
		<-ctx.Done()
		calls = append(calls, "worker.done")
		return ctx.Err()
	})
	database := &recordingCloseComponent{name: "database.close", calls: &calls}

	err := stopRuntime(httpServer, worker, database, 20*time.Millisecond)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopRuntime() error = %v, want deadline exceeded", err)
	}
	if got := strings.Join(calls, ","); got != "http,worker.cancel,worker.done,database.close" {
		t.Fatalf("runtime stop order = %q", got)
	}
}

func TestOnceCloseComponentClosesUnderlyingComponentOnce(t *testing.T) {
	closeError := errors.New("close")
	calls := make([]string, 0, 1)
	underlying := &recordingCloseComponent{
		name:  "database.close",
		calls: &calls,
		err:   closeError,
	}
	closer := &onceCloseComponent{component: underlying}

	for attempt := 0; attempt < 2; attempt++ {
		if err := closer.Close(); !errors.Is(err, closeError) {
			t.Fatalf("Close() attempt %d error = %v", attempt, err)
		}
	}
	if got := strings.Join(calls, ","); got != "database.close" {
		t.Fatalf("close calls = %q, want one call", got)
	}
}

type shutdownComponentFunc func(context.Context) error

func (f shutdownComponentFunc) Shutdown(ctx context.Context) error {
	return f(ctx)
}

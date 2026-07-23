package ingestion

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Runner interface {
	Run(context.Context, uuid.UUID) error
}

type Executor struct {
	jobs        chan uuid.UUID
	slots       chan struct{}
	runner      Runner
	repository  Repository
	taskTimeout time.Duration
	workers     int

	mu         sync.RWMutex
	started    bool
	stopping   bool
	aborted    bool
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	workerDone chan struct{}
	pending    map[*Reservation]struct{}
	scheduled  map[uuid.UUID]struct{}
	active     int
	idle       chan struct{}
}

type Reservation struct {
	once     sync.Once
	executor *Executor
}

type scheduleResult uint8

const (
	scheduleAccepted scheduleResult = iota
	scheduleAlreadyPresent
	scheduleFull
	scheduleUnavailable
)

func NewExecutor(
	workers int,
	queueCapacity int,
	taskTimeout time.Duration,
	runner Runner,
	repository Repository,
) (*Executor, error) {
	if workers <= 0 {
		return nil, errors.New("RAG 导入 worker 数必须大于 0")
	}
	if queueCapacity < 0 {
		return nil, errors.New("RAG 导入队列容量不能小于 0")
	}
	if taskTimeout <= 0 {
		return nil, errors.New("RAG 导入任务超时必须大于 0")
	}
	if runner == nil {
		return nil, errors.New("缺少 RAG 导入任务执行器")
	}
	if repository == nil {
		return nil, errors.New("缺少 RAG 导入仓储")
	}

	totalCapacity := workers + queueCapacity
	idle := make(chan struct{})
	close(idle)
	return &Executor{
		jobs:        make(chan uuid.UUID, totalCapacity),
		slots:       make(chan struct{}, totalCapacity),
		runner:      runner,
		repository:  repository,
		taskTimeout: taskTimeout,
		workers:     workers,
		workerDone:  make(chan struct{}),
		pending:     make(map[*Reservation]struct{}),
		scheduled:   make(map[uuid.UUID]struct{}),
		idle:        idle,
	}, nil
}

func (e *Executor) Start(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started || e.stopping {
		return
	}
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.started = true
	e.wg.Add(e.workers)
	for index := 0; index < e.workers; index++ {
		go e.worker()
	}
	go func() {
		e.wg.Wait()
		close(e.workerDone)
	}()
}

func (e *Executor) TryReserve() (*Reservation, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started || e.stopping || e.aborted || e.ctx.Err() != nil {
		return nil, false
	}
	select {
	case e.slots <- struct{}{}:
	default:
		return nil, false
	}

	if e.active == 0 {
		e.idle = make(chan struct{})
	}
	e.active++
	reservation := &Reservation{executor: e}
	e.pending[reservation] = struct{}{}
	return reservation, true
}

func (e *Executor) ensureScheduled(ingestionID uuid.UUID) scheduleResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.scheduled[ingestionID]; ok {
		return scheduleAlreadyPresent
	}
	if !e.started || e.stopping || e.aborted || e.ctx.Err() != nil {
		return scheduleUnavailable
	}
	select {
	case e.slots <- struct{}{}:
	default:
		return scheduleFull
	}
	if e.active == 0 {
		e.idle = make(chan struct{})
	}
	e.active++
	e.scheduled[ingestionID] = struct{}{}
	e.jobs <- ingestionID
	return scheduleAccepted
}

func (e *Executor) IsScheduled(ingestionID uuid.UUID) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.scheduled[ingestionID]
	return ok
}

func (r *Reservation) Commit(ingestionID uuid.UUID) bool {
	if r == nil || r.executor == nil {
		return false
	}
	committed := false
	r.once.Do(func() {
		committed = r.executor.commit(r, ingestionID)
	})
	return committed
}

func (r *Reservation) Release() {
	if r == nil || r.executor == nil {
		return
	}
	r.once.Do(func() {
		r.executor.releaseReservation(r)
	})
}

func (e *Executor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	if !e.started {
		e.stopping = true
		e.mu.Unlock()
		return nil
	}
	e.stopping = true
	idle := e.idle
	if e.active == 0 {
		e.cancel()
	}
	workerDone := e.workerDone
	e.mu.Unlock()

	select {
	case <-idle:
	case <-ctx.Done():
		e.abort()
		return ctx.Err()
	}

	select {
	case <-workerDone:
		return nil
	case <-ctx.Done():
		e.abort()
		return ctx.Err()
	}
}

func (e *Executor) commit(reservation *Reservation, ingestionID uuid.UUID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.pending[reservation]; !ok {
		return false
	}
	delete(e.pending, reservation)
	if e.aborted || e.ctx.Err() != nil {
		e.finishLocked()
		return false
	}
	if _, ok := e.scheduled[ingestionID]; ok {
		e.finishLocked()
		return true
	}
	e.scheduled[ingestionID] = struct{}{}
	// Every committed job owns one total-capacity slot, so this send cannot block.
	e.jobs <- ingestionID
	return true
}

func (e *Executor) releaseReservation(reservation *Reservation) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.pending[reservation]; !ok {
		return
	}
	delete(e.pending, reservation)
	e.finishLocked()
}

func (e *Executor) finishJob(ingestionID uuid.UUID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.scheduled, ingestionID)
	e.finishLocked()
}

func (e *Executor) finishLocked() {
	if e.active <= 0 {
		return
	}
	e.active--
	<-e.slots
	if e.active == 0 {
		close(e.idle)
		if e.stopping && e.cancel != nil {
			e.cancel()
		}
	}
}

func (e *Executor) abort() {
	e.mu.Lock()
	if e.aborted {
		e.mu.Unlock()
		return
	}
	e.aborted = true
	e.stopping = true
	if e.cancel != nil {
		e.cancel()
	}
	pending := make([]*Reservation, 0, len(e.pending))
	for reservation := range e.pending {
		pending = append(pending, reservation)
	}
	e.mu.Unlock()

	for _, reservation := range pending {
		reservation.Release()
	}
}

package app

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"cfw-policy-llm-agent/server/internal/storage"
)

type RunFunc func(context.Context, storage.Job) error

// JobFailure exposes only stable, controlled status metadata. Raw upstream
// errors or reasoning must never be copied into a persisted event.
type JobFailure struct {
	Code    string
	Partial bool
}

func (f JobFailure) Error() string        { return f.Code }
func (f JobFailure) JobErrorCode() string { return f.Code }
func (f JobFailure) JobTerminalStatus() string {
	if f.Partial {
		return "partial"
	}
	return "failed"
}

var ErrStopping = errors.New("server stopping")

type Engine struct {
	Store     *storage.Store
	run       RunFunc
	ctx       context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.Mutex
	active    map[string]context.CancelFunc
	admission sync.RWMutex
	stopping  bool
	startOnce sync.Once
}

func New(store *storage.Store, run RunFunc) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{Store: store, run: run, ctx: ctx, stop: cancel, active: map[string]context.CancelFunc{}}
}

func (e *Engine) Start() {
	e.startOnce.Do(func() {
		e.admission.RLock()
		defer e.admission.RUnlock()
		if e.stopping {
			return
		}
		for range 3 {
			e.wg.Add(1)
			go e.worker()
		}
		e.wg.Add(1)
		go e.expireQueue()
	})
}

func (e *Engine) Stop() {
	e.admission.Lock()
	e.stopping = true
	e.stop()
	e.admission.Unlock()
	e.wg.Wait()
}

func (e *Engine) Create(ctx context.Context, conversation, request string) (storage.Job, bool, error) {
	e.admission.RLock()
	defer e.admission.RUnlock()
	if e.stopping {
		return storage.Job{}, false, ErrStopping
	}
	return e.Store.Create(ctx, conversation, request)
}

func (e *Engine) EnqueuePlan(ctx context.Context, planID, key string, configRevision ...string) (storage.Job, bool, error) {
	e.admission.RLock()
	defer e.admission.RUnlock()
	if e.stopping {
		return storage.Job{}, false, ErrStopping
	}
	return e.Store.EnqueuePlan(ctx, planID, key, configRevision...)
}
func (e *Engine) EnqueueAssistant(ctx context.Context, plan storage.ExecutionPlan, input storage.MessageInput) (storage.Job, storage.ConversationMessage, bool, error) {
	e.admission.RLock()
	defer e.admission.RUnlock()
	if e.stopping {
		return storage.Job{}, storage.ConversationMessage{}, false, ErrStopping
	}
	return e.Store.EnqueueAssistant(ctx, plan, input)
}

func (e *Engine) Cancel(ctx context.Context, id string) (storage.Job, error) {
	j, err := e.Store.Cancel(ctx, id)
	if err != nil {
		return j, err
	}
	e.mu.Lock()
	cancel := e.active[id]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return j, nil
}

func (e *Engine) worker() {
	defer e.wg.Done()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		default:
		}
		j, ok, err := e.Store.Claim(e.ctx)
		if err == nil && ok {
			e.execute(j)
			continue
		}
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *Engine) expireQueue() {
	defer e.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			_ = e.Store.ExpireQueued(e.ctx)
		}
	}
}

func (e *Engine) invoke(ctx context.Context, job storage.Job) (err error) {
	defer func() {
		if recover() != nil {
			err = JobFailure{Code: "EXECUTION_FAILED"}
		}
	}()
	if e.run == nil {
		return JobFailure{Code: "EXECUTION_NOT_CONFIGURED"}
	}
	return e.run(ctx, job)
}

func (e *Engine) execute(j storage.Job) {
	maximum := 240 * time.Second
	switch j.Kind {
	case "assistant_turn", "collect":
		maximum = 90 * time.Second
	case "connection_check", "e0_probe":
		maximum = 30 * time.Second
	}
	deadline := time.Now().Add(maximum)
	if stored, err := time.Parse(time.RFC3339Nano, j.ExecutionDeadline); err == nil && stored.Before(deadline) {
		deadline = stored
	}
	ctx, cancel := context.WithDeadline(e.ctx, deadline)
	e.mu.Lock()
	e.active[j.ID] = cancel
	e.mu.Unlock()
	current, err := e.Store.Get(ctx, j.ID)
	if err == nil && current.Status == "running" {
		err = e.invoke(ctx, j)
	} else if err == nil {
		err = context.Canceled
	}
	if err == nil {
		err = ctx.Err()
	}
	status, code := "completed", ""
	if err != nil {
		status, code = "failed", "EXECUTION_FAILED"
		var failure interface {
			JobErrorCode() string
			JobTerminalStatus() string
		}
		if errors.As(err, &failure) {
			status, code = failure.JobTerminalStatus(), failure.JobErrorCode()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = "JOB_DEADLINE_EXCEEDED"
		} else if errors.Is(err, context.Canceled) {
			code = "JOB_CANCELLED"
		}
		if (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) && status != "partial" {
			readCtx, finish := context.WithTimeout(context.Background(), 5*time.Second)
			latest, readErr := e.Store.Get(readCtx, j.ID)
			finish()
			if readErr == nil && latest.ResultRef != "" {
				status = "partial"
			}
		}
	}
	if e.ctx.Err() != nil {
		status, code = "interrupted", "JOB_INTERRUPTED"
	}
	cancel()
	// Persist using a bounded service context, never an already-cancelled request.
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	// A failed commit leaves the last durable status visible; no success is sent.
	if err := e.Store.FinishWithCode(saveCtx, j.ID, status, code); err != nil {
		log.Print("terminal state could not be persisted")
	}
	saveCancel()
	e.mu.Lock()
	delete(e.active, j.ID)
	e.mu.Unlock()
}

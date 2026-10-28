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
}

func New(store *storage.Store, run RunFunc) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{Store: store, run: run, ctx: ctx, stop: cancel, active: map[string]context.CancelFunc{}}
}

func (e *Engine) Start() {
	for range 3 {
		e.wg.Add(1)
		go e.worker()
	}
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

func (e *Engine) execute(j storage.Job) {
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	e.mu.Lock()
	e.active[j.ID] = cancel
	e.mu.Unlock()
	current, err := e.Store.Get(ctx, j.ID)
	if err == nil && current.Status == "running" {
		err = e.run(ctx, j)
	}
	status := "completed"
	if err != nil {
		status = "failed"
	}
	if e.ctx.Err() != nil {
		status = "interrupted"
	}
	cancel()
	// Persist using a bounded service context, never an already-cancelled request.
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	// A failed commit leaves the last durable status visible; no success is sent.
	if err := e.Store.Finish(saveCtx, j.ID, status); err != nil {
		log.Print("E0 terminal state could not be persisted")
	}
	saveCancel()
	e.mu.Lock()
	delete(e.active, j.ID)
	e.mu.Unlock()
}

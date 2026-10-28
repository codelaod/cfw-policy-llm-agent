package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/storage"
)

func enginePlan(t *testing.T, s *storage.Store, title string) storage.ExecutionPlan {
	t.Helper()
	in, err := audit.SyntheticExample("internet-out")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := s.CreateConversation(context.Background(), title, in.Scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	frozen, heads, err := s.FreezeInput(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.SavePlan(context.Background(), storage.ExecutionPlan{Kind: "connection_check", ConversationID: conversation.ID, DataSourceID: "local-offline", PolicyScopeRef: in.Scope.Ref, SnapshotID: snapshot.ID, ConversationRevision: conversation.Revision, Mode: "local_probe", PreparedInput: frozen, Heads: heads, ConfigRevision: "offline-v1"})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func awaitEngineJob(t *testing.T, s *storage.Store, id, status string) storage.Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == status {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s status %s, expected %s", id, job.Status, status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEngineSharesThreeSlotsAcrossProbeAndPlannedJobs(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "engine.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	arrivals := make(chan string, 4)
	var mu sync.Mutex
	gates := map[string]chan struct{}{}
	engine := New(s, func(ctx context.Context, job storage.Job) error {
		gate := make(chan struct{})
		mu.Lock()
		gates[job.ID] = gate
		mu.Unlock()
		arrivals <- job.ID
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gate:
			return nil
		}
	})
	engine.Start()
	engine.Start()
	defer engine.Stop()
	probe, _, err := engine.Create(context.Background(), "legacy-probe", "legacy-request")
	if err != nil {
		t.Fatal(err)
	}
	plans := []storage.ExecutionPlan{enginePlan(t, s, "会话A"), enginePlan(t, s, "会话B"), enginePlan(t, s, "会话C")}
	jobs := []storage.Job{probe}
	for i, plan := range plans {
		job, _, err := engine.EnqueuePlan(context.Background(), plan.ID, []string{"a", "b", "c"}[i])
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	first := []string{}
	for range 3 {
		select {
		case id := <-arrivals:
			first = append(first, id)
		case <-time.After(time.Second):
			t.Fatal("three actual runners did not overlap")
		}
	}
	select {
	case <-arrivals:
		t.Fatal("fourth runner exceeded shared three slots")
	case <-time.After(50 * time.Millisecond):
	}
	mu.Lock()
	close(gates[first[1]])
	mu.Unlock()
	select {
	case <-arrivals:
	case <-time.After(time.Second):
		t.Fatal("released slot did not claim fourth task")
	}
	mu.Lock()
	for id, gate := range gates {
		if id != first[1] {
			close(gate)
		}
	}
	mu.Unlock()
	for _, job := range jobs {
		awaitEngineJob(t, s, job.ID, "completed")
	}
}

func TestEnginePartialCancellationAndShutdownAdmission(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "engine.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	engine := New(s, func(context.Context, storage.Job) error {
		return JobFailure{Code: "MODEL_NOT_CONFIGURED", Partial: true}
	})
	engine.Start()
	plan := enginePlan(t, s, "部分结果")
	job, _, err := engine.EnqueuePlan(context.Background(), plan.ID, "partial")
	if err != nil {
		t.Fatal(err)
	}
	got := awaitEngineJob(t, s, job.ID, "partial")
	if got.ErrorCode != "MODEL_NOT_CONFIGURED" {
		t.Fatal("controlled partial error lost")
	}
	engine.Stop()
	if _, _, err = engine.EnqueuePlan(context.Background(), plan.ID, "after-stop"); !errors.Is(err, ErrStopping) {
		t.Fatal("stopped engine accepted a new plan")
	}
}

func TestAuditRunnerCannotClaimCompletionWithoutSavedRules(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "empty-result.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	plan := enginePlan(t, s, "未保存结果")
	plan.ID = ""
	plan.Kind = "audit"
	plan.Mode = "rules_only"
	plan.ConsumedJobID = ""
	plan, err = s.SavePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	engine := New(s, func(context.Context, storage.Job) error { return nil })
	engine.Start()
	defer engine.Stop()
	job, _, err := engine.EnqueuePlan(context.Background(), plan.ID, "empty-result")
	if err != nil {
		t.Fatal(err)
	}
	got := awaitEngineJob(t, s, job.ID, "failed")
	if got.ErrorCode != "RESULT_NOT_SAVED" {
		t.Fatal("audit was called complete without durable result")
	}
}

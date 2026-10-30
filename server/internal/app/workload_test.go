package app

import (
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func explicitWorkloads(t *testing.T) workload.Config {
	t.Helper()
	profiles := map[string]workload.Limits{}
	for _, kind := range []string{"audit", "assistant_turn", "collect", "connection_check"} {
		l := workload.Default(kind)
		if l.FinalizeSeconds == 0 {
			l.FinalizeSeconds = 10
			if l.ModelSeconds > 0 {
				l.ModelSeconds -= 10
			} else {
				l.CollectionSeconds -= 10
			}
		}
		if kind == "audit" {
			l.ModelLogicalCalls = 12
			l.ModelHTTPAttempts = 14
			l.ModelRequestSeconds = 60
		}
		profiles[kind] = l
	}
	b, _ := json.Marshal(map[string]any{"schema_version": 1, "profile_ref": "explicit-plans", "profiles": profiles})
	p := filepath.Join(t.TempDir(), "execution.json")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := workload.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestPlanResolvesExplicitProfileAndStricterPrivateLimits(t *testing.T) {
	_, service, _, _, request := planCase(t)
	service.Workloads = explicitWorkloads(t)
	service.ModelConfigured = true
	service.ModelReady = true
	service.ModelHTTPAttempts = 9
	service.ModelTimeout = 45 * time.Second
	request.Mode = "rules_and_agent"
	plan, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Workload.Legacy || plan.Workload.Limits.ModelLogicalCalls != 12 || plan.Workload.Limits.ModelHTTPAttempts != 9 || plan.Workload.Limits.ModelRequestSeconds != 45 || plan.EffectCeiling.ModelHTTPAttempts != 9 {
		t.Fatal("plan coupled logical and physical or kept hidden caps")
	}
	service.ModelHTTPAttempts = 0
	plan, err = service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Workload.Limits.ModelHTTPAttempts != 0 || plan.EffectCeiling.AllowModelRequests || plan.EffectCeiling.ModelHTTPAttempts != 0 {
		t.Fatal("explicit zero model allowance fell back")
	}
}

func TestEngineUsesFrozenExecutionWindowInsteadOfKindConstant(t *testing.T) {
	store, service, _, _, request := planCase(t)
	preview, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	p := preview.ExecutionPlan
	p.ID = ""
	p.CreatedAt = ""
	p.ExpiresAt = ""
	p.ConsumedJobID = ""
	l := workload.Default("audit")
	l.ExecutionSeconds = 600
	l.CollectionSeconds = 180
	l.RulesSeconds = 40
	l.ModelSeconds = 360
	l.FinalizeSeconds = 20
	p.Workload = workload.Resolved{ProfileRef: "explicit-engine", Limits: l}
	saved, err := store.SavePlan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan time.Duration, 1)
	engine := New(store, func(ctx context.Context, job storage.Job) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return JobFailure{Code: "MISSING_DEADLINE"}
		}
		seen <- time.Until(deadline)
		return nil
	})
	defer engine.Stop()
	if _, _, err = engine.EnqueuePlan(context.Background(), saved.ID, "frozen-engine", service.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	engine.Start()
	select {
	case remaining := <-seen:
		if remaining < 590*time.Second || remaining > 600*time.Second {
			t.Fatal("execution still cut by legacy 240 seconds", remaining)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("task did not start")
	}
}

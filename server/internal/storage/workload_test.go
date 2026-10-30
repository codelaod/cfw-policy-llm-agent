package storage

import (
	"cfw-policy-llm-agent/server/internal/workload"
	"context"
	"reflect"
	"testing"
	"time"
)

func TestFrozenWorkloadControlsPersistentQueueAndExecutionDeadlines(t *testing.T) {
	s, _, _, p := executionCase(t)
	limits := workload.Default("audit")
	limits.QueueSeconds = 7
	limits.ExecutionSeconds = 600
	limits.CollectionSeconds = 180
	limits.RulesSeconds = 40
	limits.ModelSeconds = 360
	limits.FinalizeSeconds = 20
	limits.ModelRequestSeconds = 60
	limits.ModelLogicalCalls = 12
	limits.ModelHTTPAttempts = 12
	limits.CloudHTTPAttempts = 1000
	p.Workload = workload.Resolved{ProfileRef: "explicit-workload", Limits: limits}
	p.Mode = "rules_and_agent"
	p.EffectCeiling = EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: 12}
	saved, err := s.SavePlan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := s.GetPlan(context.Background(), saved.ID)
	if err != nil || !reflect.DeepEqual(persisted.Workload, p.Workload) {
		t.Fatal("frozen workload not preserved", err)
	}
	j, _, err := s.EnqueuePlan(context.Background(), saved.ID, "explicit-workload-job")
	if err != nil {
		t.Fatal(err)
	}
	accepted, _ := time.Parse(time.RFC3339Nano, j.CreatedAt)
	queued, _ := time.Parse(time.RFC3339Nano, j.QueueDeadline)
	root, _ := time.Parse(time.RFC3339Nano, j.RootDeadline)
	if queued.Sub(accepted) != 7*time.Second || root.Sub(accepted) != 607*time.Second {
		t.Fatal("kind constants replaced frozen timing")
	}
	running, found, err := s.Claim(context.Background())
	if err != nil || !found {
		t.Fatal("claim failed", err)
	}
	started, _ := time.Parse(time.RFC3339Nano, running.StartedAt)
	deadline, _ := time.Parse(time.RFC3339Nano, running.ExecutionDeadline)
	if deadline.Sub(started) != 600*time.Second || running.Workload.Limits.ModelHTTPAttempts != 12 {
		t.Fatal("claim replaced accepted profile")
	}
	read, err := s.Get(context.Background(), j.ID)
	if err != nil || read.Workload.ProfileRef != "explicit-workload" {
		t.Fatal("job read lost frozen profile", err)
	}
}
func TestOldPlanGetsExplicitLegacyAndEffectsCannotExceedFrozenProfile(t *testing.T) {
	s, _, _, p := executionCase(t)
	saved, err := s.SavePlan(context.Background(), p)
	if err != nil || !saved.Workload.Legacy || saved.Workload.ProfileRef != workload.LegacyProfileRef {
		t.Fatal("old plan not labelled legacy", err)
	}
	p.Mode = "rules_and_agent"
	limits := workload.Default("audit")
	limits.ModelHTTPAttempts = 0
	p.Workload = workload.Resolved{ProfileRef: "explicit-zero", Limits: limits}
	p.EffectCeiling = EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: 1}
	if _, err = s.SavePlan(context.Background(), p); err == nil {
		t.Fatal("effects escaped explicit zero workload")
	}
}

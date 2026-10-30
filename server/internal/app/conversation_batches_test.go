package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cfw-policy-llm-agent/server/internal/workload"
)

func batchDialogProfile(t *testing.T, rules int) workload.Config {
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
			l.AssetsPerBatch = 2
			l.RulesSeconds = rules
		}
		profiles[kind] = l
	}
	b, _ := json.Marshal(map[string]any{"schema_version": 1, "profile_ref": "batch-conversation-fixture", "profiles": profiles})
	path := filepath.Join(t.TempDir(), "execution.json")
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	cfg, e := workload.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	return cfg
}
func TestDialogueBatchAcceptanceProcessesWholeSetWithDistinctSegmentEvidence(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 5)
	s.Plans.Workloads = batchDialogProfile(t, 20)
	need := goalTurn(t, s, c.ID, "needs-batch", "检查这些对象的入向，只运行规则")
	if need.Intent.Status != "needs_batch" || len(need.Intent.Selection) != 5 || need.Proposal.PlanID != "" {
		t.Fatal("batch bound silently truncated or executed", need.Intent)
	}
	offered := goalTurn(t, s, c.ID, "offer-batch", "按完整对象集分批审计")
	if offered.Proposal == nil || offered.Proposal.PlanID == "" {
		t.Fatal("no executable batch preview", offered.Intent)
	}
	p, e := s.Store.GetPlan(context.Background(), offered.Proposal.PlanID)
	if e != nil || p.Batch == nil || len(p.Batch.Resources) != 5 || p.Batch.Size != 2 {
		t.Fatal("preview lost full set", p.Batch, e)
	}
	accept, e := s.Store.AcceptIntentProposal(context.Background(), c.ID, offered.Proposal.ID, "accept-batch", s.Plans.ConfigRevision, offered.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	parent, found, e := s.Store.Claim(context.Background())
	if e != nil || !found || parent.ID != accept.Job.ID {
		t.Fatal(e)
	}
	if e = NewWorkflow(s.Store).Run(context.Background(), parent); e != nil {
		t.Fatal("batch workflow failed", e)
	}
	result, e := s.Store.ReadJobWorkspace(context.Background(), parent.ID)
	if e != nil || result.Job.Status != "completed" || result.Job.ResultRef != "" || result.Audit != nil || result.Batch == nil || len(result.Batch.Covered) != 5 || len(result.Batch.Segments) != 3 || len(result.Batch.Unprocessed) != 0 {
		t.Fatal("parent invented merged input/result or lost coverage", e, result.Job, result.Batch)
	}
	manifests := 0
	for _, seg := range result.Batch.Segments {
		if seg.Status != "completed" || seg.Manifest == nil || seg.AuditRef == "" || seg.ReportRef == "" {
			t.Fatal("segment missing actual proof", seg)
		}
		child, e := s.Store.Get(context.Background(), seg.ChildJobID)
		if e != nil || child.ParentJobID != parent.ID || child.Status != "completed" {
			t.Fatal("internal segment became extra queued work", e)
		}
		manifests++
	}
	if manifests != 3 {
		t.Fatal("missing distinct segment manifests")
	}
	jobs, e := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
	if e != nil || len(jobs) != 1 || jobs[0].ID != parent.ID {
		t.Fatal("internal segments occupied public task slots", e)
	}
	usage, e := s.Store.BudgetUsage(context.Background(), s.Plans.BudgetRef, "", parent.ID)
	if e != nil || usage.CloudAttempts != 0 || usage.ModelAttempts != 0 {
		t.Fatal("pinned batches made provider requests", e)
	}
}
func TestDialogueBatchExhaustionKeepsActualCoveredAndUnprocessedSets(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 5)
	s.Plans.Workloads = batchDialogProfile(t, 1)
	offered := goalTurn(t, s, c.ID, "bounded-batch", "按完整对象集分批审计")
	if offered.Proposal == nil || offered.Proposal.PlanID == "" {
		t.Fatal(offered.Intent)
	}
	_, e := s.Store.AcceptIntentProposal(context.Background(), c.ID, offered.Proposal.ID, "bounded-batch-accept", s.Plans.ConfigRevision, offered.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	parent, found, e := s.Store.Claim(context.Background())
	if e != nil || !found {
		t.Fatal(e)
	}
	e = NewWorkflow(s.Store).Run(context.Background(), parent)
	if e == nil {
		t.Fatal("explicit small cumulative protection was refilled")
	}
	result, e := s.Store.ReadJobWorkspace(context.Background(), parent.ID)
	if e != nil || result.Batch == nil || result.Job.Status != "partial" || len(result.Batch.Covered) != 2 || len(result.Batch.Unprocessed) != 3 || result.Job.ResultRef != "" {
		t.Fatal("partial batch pretended whole set passed", e, result.Job, result.Batch)
	}
	if result.Batch.Usage.Phases.RulesMillis <= 0 {
		t.Fatal("phase use missing")
	}
	var completed int
	for _, seg := range result.Batch.Segments {
		if seg.Status == "completed" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatal("extra segment ignored cumulative phase bound")
	}
}

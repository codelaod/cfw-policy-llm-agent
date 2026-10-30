package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/workload"
	"gorm.io/gorm"
)

func batchCase(t *testing.T, model bool) (*Store, ExecutionPlan, Job, domain.Input) {
	t.Helper()
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "batch.sqlite"), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = migrateBatchSchema(s.db); e != nil {
		t.Fatal(e)
	}
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	base := in.Assets[0]
	base.Services = []domain.Service{}
	base.Allowed = []domain.Access{}
	base.AllowedConfirmed, base.AllowedComplete = false, false
	in.Assets = []domain.Asset{}
	in.Selection.AssetIDs = []string{}
	in.Baseline = audit.ProjectBaseline()
	resources := []PreparedResource{}
	for i := 0; i < 5; i++ {
		a := base
		a.ID = fmt.Sprintf("asset-batch-%03d", i)
		a.Address = fmt.Sprintf("192.0.2.%d", i+1)
		in.Assets = append(in.Assets, a)
		in.Selection.AssetIDs = append(in.Selection.AssetIDs, a.ID)
		resources = append(resources, PreparedResource{AssetID: a.ID, ResourceType: "ecs"})
	}
	snap, e := s.SaveSnapshot(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	frozen, heads, e := s.FreezeSelection(ctx, snap.ID, in.Selection)
	if e != nil {
		t.Fatal(e)
	}
	conv, e := s.CreateConversation(ctx, "complete finite set", in.Scope.Ref)
	if e != nil {
		t.Fatal(e)
	}
	limits := workload.Default("audit")
	limits.AssetsPerBatch = 2
	mode := "rules_only"
	effects := EffectCeiling{}
	if model {
		mode = "rules_and_agent"
		limits.ModelLogicalCalls = 3
		limits.ModelHTTPAttempts = 3
		limits.ToolCalls = 5
		limits.ToolBatch = 4
		effects = EffectCeiling{AllowModelRequests: true, ModelHTTPAttempts: 3}
	}
	p, e := s.SavePlan(ctx, ExecutionPlan{Batch: &BatchSpec{Resources: resources, Size: 2}, Workload: workload.Resolved{ProfileRef: "public-batch-fixture", Limits: limits}, Kind: "audit", ConversationID: conv.ID, ConversationRevision: conv.Revision, DataSourceID: "local-offline", PolicyScopeRef: in.Scope.Ref, SnapshotID: snap.ID, Mode: mode, BudgetRef: "batch-budget", ConfigRevision: "batch-config", PreparedInput: frozen, Heads: heads, EffectCeiling: effects})
	if e != nil {
		t.Fatal("preview full batch", e)
	}
	parent, _, e := s.EnqueuePlan(ctx, p.ID, "accept-entire-set")
	if e != nil {
		t.Fatal("accept full batch", e)
	}
	replay, same, e := s.EnqueuePlan(ctx, p.ID, "accept-entire-set")
	if e != nil || !same || replay.ID != parent.ID {
		t.Fatal("original acceptance not idempotent", e)
	}
	parent, found, e := s.Claim(ctx)
	if e != nil || !found {
		t.Fatal("claim parent", e)
	}
	return s, p, parent, in
}
func finishBatchAudit(t *testing.T, s *Store, child Job, models, tools int, missingRules bool) {
	t.Helper()
	ctx := context.Background()
	p, e := s.GetPlan(ctx, child.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	input := p.PreparedInput
	input.EvaluationAt = time.Now().UTC().Format(time.RFC3339Nano)
	sealed, e := s.SealInput(ctx, child.ID, input)
	if e != nil {
		t.Fatal("seal segment", e)
	}
	if !missingRules {
		if e = s.AppendJobEvent(ctx, child.ID, "stage_changed", map[string]any{"stage": "rules_started"}); e != nil {
			t.Fatal(e)
		}
	}
	bounded, e := audit.WithExecutionLimits(ctx, audit.ExecutionLimits{MaxAssets: p.Workload.Limits.AssetsPerBatch, MaxPolicies: p.Workload.Limits.MaxPolicies, MaxQueryCells: p.Workload.Limits.MaxQueryCells, Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	result, e := audit.Run(bounded, sealed)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := s.SaveJobAudit(ctx, child.ID, result)
	if e != nil {
		t.Fatal("save segment audit", e)
	}
	status, code := "completed", ""
	if models > 0 {
		if e = s.AppendJobEvent(ctx, child.ID, "stage_changed", map[string]any{"stage": "agent_started"}); e != nil {
			t.Fatal(e)
		}
		_, e = s.SaveJobAgentResult(ctx, child.ID, AgentContent{JobID: child.ID, Manifest: manifestFor(sealed), ModelCalls: models, ToolCalls: tools, ErrorCode: "AGENT_EXECUTION_FAILED", Context: ContextCoverage{DisclosureVersion: "minimal-v1"}})
		if e != nil {
			t.Fatal("save recorded logical usage", e)
		}
		status, code = "partial", "AGENT_EXECUTION_FAILED"
	}
	if e = s.AppendJobEvent(ctx, child.ID, "stage_changed", map[string]any{"stage": "report_building"}); e != nil {
		t.Fatal(e)
	}
	_, heads, e := s.CaptureReportInput(ctx, saved.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.FinalizeJobWithReport(ctx, child.ID, FinalizeJobReportRequest{Format: "json", Heads: heads, Content: []byte(`{"source":"synthetic","batch_segment_fixture":true}`), Key: "report-" + child.ID, Status: status, Code: code})
	if e != nil {
		t.Fatal("finalize segment", e)
	}
}
func TestBatchPinnedSerialSegmentsKeepOwnEvidenceAndOneRootSlot(t *testing.T) {
	s, p, parent, _ := batchCase(t, false)
	ctx := context.Background()
	before, e := s.GetPlan(ctx, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	request, e := s.GetBatchRequest(ctx, parent.ID)
	if e != nil || len(request.Spec.Resources) != 5 || len(request.Segments) != 3 {
		t.Fatal(request, e)
	}
	ids := map[string]bool{}
	for ordinal := 1; ordinal <= 3; ordinal++ {
		child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
		if e != nil || !found || child.ParentJobID != parent.ID || child.Status != "running" {
			t.Fatal("serial internal start", child, found, e)
		}
		if _, _, e = s.StartNextBatchSegment(ctx, parent.ID); !errors.Is(e, ErrBusy) {
			t.Fatal("parallel segment accepted", e)
		}
		if child.RootDeadline > parent.RootDeadline || child.ExecutionDeadline > parent.ExecutionDeadline || child.BudgetRef != parent.BudgetRef {
			t.Fatal("segment extended root authority")
		}
		finishBatchAudit(t, s, child, 0, 0, false)
		seg, e := s.RefreshBatchSegment(ctx, parent.ID, ordinal)
		if e != nil || seg.Manifest == nil || seg.ReportRef == "" || seg.AuditRef == "" {
			t.Fatal("segment evidence missing", seg, e)
		}
		if ids[seg.ReportRef] {
			t.Fatal("segment report reused")
		}
		ids[seg.ReportRef] = true
	}
	request, e = s.GetBatchRequest(ctx, parent.ID)
	if e != nil || request.Status != "completed" || len(request.Covered) != 5 || len(request.Failed) != 0 || len(request.Unprocessed) != 0 {
		t.Fatal("whole-set coverage", request, e)
	}
	if _, found, e := s.StartNextBatchSegment(ctx, parent.ID); e != nil || found {
		t.Fatal("completed batch restarted", found, e)
	}
	jobs, e := s.ListJobs(ctx, parent.ConversationID, 0, 100)
	if e != nil || len(jobs) != 1 || jobs[0].ID != parent.ID {
		t.Fatal("internal jobs leaked into main task list", len(jobs), e)
	}
	var queued int64
	s.db.Raw("SELECT count(*) FROM e0_jobs WHERE parent_job_id IS NOT NULL AND status='queued'").Scan(&queued)
	if queued != 0 {
		t.Fatal("internal work entered queue")
	}
	if e = s.FinishWithCode(ctx, parent.ID, "completed", ""); e != nil {
		t.Fatal(e)
	}
	ended, e := s.Get(ctx, parent.ID)
	if e != nil || ended.Status != "completed" || ended.ResultRef != "" {
		t.Fatal("parent invented merged audit", ended, e)
	}
	after, e := s.GetPlan(ctx, p.ID)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("original accepted plan mutated", e)
	}
}
func TestBatchPinnedFreezesBusinessAndDoesNotReadLaterGoal(t *testing.T) {
	s, p, parent, in := batchCase(t, false)
	ctx := context.Background()
	insertPreparedProfile(t, s, in, p.SnapshotID, "later-profile", "database", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano))
	if e := s.db.Exec("INSERT INTO asset_heads(scope_key,asset_key,revision,profile_version_id) VALUES(?,?,1,?)", canonicalBusinessScope(in.Scope), in.Assets[0].ID, "later-profile").Error; e != nil {
		t.Fatal(e)
	}
	if e := s.db.Exec("UPDATE conversations SET revision=revision+1,archived=1 WHERE id=?", parent.ConversationID).Error; e != nil {
		t.Fatal(e)
	}
	child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
	if e != nil || !found {
		t.Fatal("accepted parent authority replaced by later head/conversation", e)
	}
	cp, e := s.GetPlan(ctx, child.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	for _, a := range cp.PreparedInput.Assets {
		if len(a.Services) != 0 {
			t.Fatal("post-acceptance business confirmation entered segment")
		}
	}
	window, e := s.ReadAgentContextWindow(ctx, child.ID)
	if e != nil {
		t.Fatal("child cannot use original accepted context", e)
	}
	var original ConversationTurn
	s.db.Raw("SELECT * FROM conversation_turns WHERE job_id=?", parent.ID).Scan(&original)
	if window.AcceptedMessageID != original.MessageID {
		t.Fatal("child changed accepted message position")
	}
}
func TestBatchCountersAndPhaseLimitsNeverResetAtNextSegment(t *testing.T) {
	s, _, parent, _ := batchCase(t, true)
	ctx := context.Background()
	if e := s.ConfigureBudgetPolicy(ctx, BudgetPolicy{Ref: parent.BudgetRef, ApplicationModelAttempts: 20, SessionModelAttempts: 20}); e != nil {
		t.Fatal(e)
	}
	first, found, e := s.StartNextBatchSegment(ctx, parent.ID)
	if e != nil || !found {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e = s.ReserveAttempt(ctx, first.ID, "model", "configured-model", "batch-config"); e != nil {
			t.Fatal(e)
		}
	}
	finishBatchAudit(t, s, first, 2, 3, false)
	second, found, e := s.StartNextBatchSegment(ctx, parent.ID)
	if e != nil || !found {
		t.Fatal("remaining segment", e)
	}
	if second.Workload.Limits.ModelLogicalCalls != 1 || second.Workload.Limits.ToolCalls != 2 || second.Workload.Limits.ToolBatch != 2 || second.Workload.Limits.ModelHTTPAttempts != 1 {
		t.Fatal("cumulative allowance reset", second.Workload)
	}
	if _, e = s.ReserveAttempt(ctx, second.ID, "model", "configured-model", "batch-config"); e != nil {
		t.Fatal("last physical attempt denied", e)
	}
	if _, e = s.ReserveAttempt(ctx, second.ID, "model", "configured-model", "batch-config"); !errors.Is(e, ErrBudgetExhausted) {
		t.Fatal("physical root ceiling reset", e)
	}
	finishBatchAudit(t, s, second, 1, 2, false)
	if _, found, e = s.StartNextBatchSegment(ctx, parent.ID); e != nil || found {
		t.Fatal("exhausted root started a new segment", found, e)
	}
	value, e := s.GetBatchRequest(ctx, parent.ID)
	if e != nil || value.ErrorCode != "BATCH_LIMIT_EXHAUSTED" || value.Usage.ModelCalls != 3 || value.Usage.ToolCalls != 5 || value.Usage.ModelAttempts != 3 || len(value.Unprocessed) != 1 {
		t.Fatal("lost total or unprocessed coverage", value, e)
	}
}
func TestBatchUnknownLogicalOrPhaseUsageStopsRatherThanRefills(t *testing.T) {
	for _, kind := range []string{"logical", "phase"} {
		t.Run(kind, func(t *testing.T) {
			s, _, parent, _ := batchCase(t, kind == "logical")
			ctx := context.Background()
			child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
			if e != nil || !found {
				t.Fatal(e)
			}
			if kind == "logical" {
				if e = s.AppendJobEvent(ctx, child.ID, "stage_changed", map[string]any{"stage": "agent_started"}); e != nil {
					t.Fatal(e)
				}
				if e = s.FinishWithCode(ctx, child.ID, "failed", "AGENT_RESULT_SAVE_FAILED"); e != nil {
					t.Fatal(e)
				}
			} else {
				finishBatchAudit(t, s, child, 0, 0, true)
			}
			if _, found, e = s.StartNextBatchSegment(ctx, parent.ID); e != nil || found {
				t.Fatal("unknown usage became fresh allowance", found, e)
			}
			value, e := s.GetBatchRequest(ctx, parent.ID)
			if e != nil || !value.Usage.UsageUnknown || value.ErrorCode != "BATCH_USAGE_UNKNOWN" || len(value.Unprocessed) == 0 {
				t.Fatal("unknown usage not retained", value, e)
			}
		})
	}
}
func TestBatchMigrationKeepsOldTurnAndNormalReplay(t *testing.T) {
	s, _, parent, _ := batchCase(t, false)
	ctx := context.Background()
	var original ConversationTurn
	s.db.Raw("SELECT * FROM conversation_turns WHERE job_id=?", parent.ID).Scan(&original)
	// Reproduce the exact pre-batch UNIQUE message shape with real old rows.
	for _, q := range []string{`CREATE TABLE old_batch_turns(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),plan_id TEXT UNIQUE NOT NULL REFERENCES execution_plans(id),job_id TEXT UNIQUE NOT NULL REFERENCES e0_jobs(id),message_id TEXT UNIQUE NOT NULL REFERENCES conversation_messages(id),reply_message_id TEXT REFERENCES conversation_messages(id),state TEXT NOT NULL,created_at TEXT NOT NULL)`, `INSERT INTO old_batch_turns SELECT * FROM conversation_turns`, `DROP TABLE conversation_turns`, `ALTER TABLE old_batch_turns RENAME TO conversation_turns`} {
		if e := s.db.Exec(q).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := checkBatchSchema(s.db); e == nil {
		t.Fatal("old unique-message relation passed normal startup shape")
	}
	if e := migrateBatchSchema(s.db); e != nil {
		t.Fatal(e)
	}
	if e := migrateBatchSchema(s.db); e != nil {
		t.Fatal("migration replay", e)
	}
	var restored ConversationTurn
	s.db.Raw("SELECT * FROM conversation_turns WHERE job_id=?", parent.ID).Scan(&restored)
	if !reflect.DeepEqual(original, restored) {
		t.Fatal("old accepted turn changed")
	}
	child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
	if e != nil || !found {
		t.Fatal("parent acceptance could not authorize internal turn", e)
	}
	if _, e = s.ReadAgentContextWindow(ctx, child.ID); e != nil {
		t.Fatal(e)
	}
	// SQL tampering must not turn a different message into child authority.
	other, _, e := s.AppendConversationMessage(ctx, parent.ConversationID, MessageInput{ClientMessageID: "unrelated-later-message", Role: "user", Origin: "user", PublicText: "later unrelated goal", PolicyScopeRef: parentScope(t, s, parent)})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.db.Exec("UPDATE conversation_turns SET message_id=? WHERE job_id=?", other.ID, child.ID).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadAgentContextWindow(ctx, child.ID); e == nil {
		t.Fatal("unrelated accepted message authorized child")
	}
	_ = ctx
}
func TestBatchRejectsPartitionSelectionAndBusinessWidening(t *testing.T) {
	s, p, parent, _ := batchCase(t, false)
	_ = parent
	ctx := context.Background()
	p.ID, p.ConsumedJobID = "", ""
	p.ConversationID = "unknown" // Pure scope/selection validation happens before any external effects.
	for _, change := range []string{"scope", "duplicate", "size", "business"} {
		t.Run(change, func(t *testing.T) {
			raw, _ := json.Marshal(p)
			var probe ExecutionPlan
			json.Unmarshal(raw, &probe)
			probe.PreparedInput = p.PreparedInput
			probe.Heads = p.Heads
			switch change {
			case "scope":
				probe.PolicyScopeRef = "other"
			case "duplicate":
				probe.Batch.Resources[1] = probe.Batch.Resources[0]
			case "size":
				probe.Batch.Size = probe.Workload.Limits.AssetsPerBatch + 1
			case "business":
				probe.PreparedInput.Assets = append([]domain.Asset{}, p.PreparedInput.Assets...)
				probe.PreparedInput.Assets[0].AllowedConfirmed = true
				probe.PreparedInput.Assets[0].AllowedComplete = true
				probe.PreparedInput.Assets[0].DenyAll = true
			}
			e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return validateBatchPlan(tx, probe) })
			if e == nil {
				t.Fatal("invalid batch accepted", change)
			}
		})
	}
}

func parentScope(t *testing.T, s *Store, job Job) string {
	t.Helper()
	p, e := s.GetPlan(context.Background(), job.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	return p.PolicyScopeRef
}

func TestBatchCloudSegmentsInheritOriginalAcceptanceWatermarksAndHeads(t *testing.T) {
	s, prior, _, in := batchCase(t, false)
	ctx := context.Background()
	resources := []PreparedResource{}
	ids := []string{}
	for i := range in.Assets {
		in.Assets[i].ID = fmt.Sprintf("i-batch-%03d", i)
		in.Assets[i].ProviderResourceID = in.Assets[i].ID
		in.Assets[i].Region = "cn-test"
		in.Assets[i].ResourceType = "ecs"
		in.Assets[i].MetadataComplete = true
		resources = append(resources, PreparedResource{ResourceID: in.Assets[i].ID, AssetID: in.Assets[i].ID, Region: "cn-test", ResourceType: "ecs"})
		ids = append(ids, in.Assets[i].ID)
	}
	old := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertPreparedProfile(t, s, in, prior.SnapshotID, "batch-profile-before", "web", old)
	if e := s.db.Exec("INSERT INTO asset_heads(scope_key,asset_key,revision,profile_version_id) VALUES(?,?,1,?)", canonicalBusinessScope(in.Scope), in.Assets[0].ID, "batch-profile-before").Error; e != nil {
		t.Fatal(e)
	}
	grantResources := append([]PreparedResource{}, resources...)
	for i := range grantResources {
		grantResources[i].AssetID = ""
	}
	grant := ConnectionGrant{SourceRef: "batch-cloud-source", ConfigRevision: "batch-cloud-config", AccountRef: in.Scope.AccountRef, ControlPlaneRef: in.Scope.ControlPlaneRef, Regions: []string{"cn-test"}, Boundaries: []string{"internet/in"}, Assets: grantResources}
	if e := s.RegisterConnectionGrant(ctx, grant); e != nil {
		t.Fatal(e)
	}
	if e := s.RegisterConfiguredScopes(ctx, grant.SourceRef, grant.ConfigRevision, []domain.Scope{in.Scope}); e != nil {
		t.Fatal(e)
	}
	conv, e := s.CreateConversation(ctx, "cloud explicit finite batch", in.Scope.Ref)
	if e != nil {
		t.Fatal(e)
	}
	limits := workload.Default("audit")
	limits.AssetsPerBatch = 2
	p, e := s.SavePlan(ctx, ExecutionPlan{Batch: &BatchSpec{Resources: resources, Size: 2}, Workload: workload.Resolved{ProfileRef: "public-cloud-batch", Limits: limits}, Preparation: &PreparationSpec{SourceRef: grant.SourceRef, ConfigRevision: grant.ConfigRevision, Query: domain.ResourceQuery{Region: "cn-test", ResourceType: "ecs", ResourceIDs: ids}, Selected: resources, Boundary: "internet", Direction: "in", DataPolicy: "force_refresh", AllowMultiple: true, ResultLimit: len(resources)}, Kind: "audit", ConversationID: conv.ID, ConversationRevision: conv.Revision, DataSourceID: grant.SourceRef, ConfigRevision: grant.ConfigRevision, PolicyScopeRef: in.Scope.Ref, Mode: "rules_only", BudgetRef: "cloud-batch-budget", PreparedInput: domain.Input{Scope: in.Scope, Baseline: audit.ProjectBaseline()}, EffectCeiling: EffectCeiling{AllowCloudReads: true, CloudHTTPAttempts: 10}})
	if e != nil {
		t.Fatal("cloud batch preview", e)
	}
	parent, _, e := s.EnqueuePlan(ctx, p.ID, "accept-cloud-batch")
	if e != nil {
		t.Fatal(e)
	}
	claimed, found, e := s.Claim(ctx)
	if e != nil || !found || claimed.ID != parent.ID {
		t.Fatal(e)
	}
	insertPreparedProfile(t, s, in, prior.SnapshotID, "batch-profile-after", "database", old)
	if e = s.db.Exec("UPDATE asset_heads SET revision=2,profile_version_id='batch-profile-after' WHERE scope_key=? AND asset_key=?", canonicalBusinessScope(in.Scope), in.Assets[0].ID).Error; e != nil {
		t.Fatal(e)
	}
	child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
	if e != nil || !found {
		t.Fatal("cloud child start", e)
	}
	cp, e := s.GetPlan(ctx, child.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	chosen := map[string]bool{}
	for _, r := range cp.Preparation.Selected {
		chosen[r.ResourceID] = true
	}
	assets := []domain.Asset{}
	for _, a := range in.Assets {
		if chosen[a.ID] {
			assets = append(assets, a)
		}
	}
	in.Assets = assets
	in.Selection = domain.Selection{Kind: "assets", AssetIDs: []string{}}
	for _, a := range assets {
		in.Selection.AssetIDs = append(in.Selection.AssetIDs, a.ID)
	}
	in.SourceKind = "live_readonly"
	in.SnapshotVersion = "cloud-batch-artificial-observation"
	in.Semantics = domain.Semantics{ProfileID: "aliyun-cfw-internet-v1", Version: audit.ProviderSemanticsVersion, DefaultAction: "unknown", SourceRef: "T-06"}
	in.Baseline = audit.ProjectBaseline()
	in.CollectedAt = now()
	in.EvaluationAt = in.CollectedAt
	in.Completeness.ObservationConsistency = "observed_stable"
	content := CloudContent{JobID: child.ID, Kind: "audit", ScopeRef: in.Scope.Ref, SourceRef: grant.SourceRef, ConfigRevision: grant.ConfigRevision, IdentityVerified: true, ProductReady: true}
	for _, round := range []int{1, 2} {
		content.Observations = append(content.Observations, CloudObservation{Round: round, Operation: "DescribeControlPolicy", Pages: 1, StartedAt: in.CollectedAt, FinishedAt: in.CollectedAt, EnumerationComplete: true, SemanticSHA256: strings.Repeat("a", 64)})
	}
	saved, e := s.SavePreparedCloudSnapshot(ctx, child.ID, content, in)
	if e != nil {
		t.Fatal(e)
	}
	binding, e := s.BindPreparedAuditInput(ctx, child.ID, saved.Content.SnapshotID)
	if e != nil {
		t.Fatal(e)
	}
	if len(binding.Input.Assets[0].Services) != 1 || binding.Input.Assets[0].Services[0].Role != "web" {
		t.Fatal("post-parent confirmation entered cloud segment", binding.Input.Assets[0].Services)
	}
	var a preparedAcceptance
	s.db.Raw("SELECT * FROM prepared_audit_acceptance WHERE job_id=?", child.ID).Scan(&a)
	if a.AcceptedAt != parent.CreatedAt || child.CreatedAt != parent.CreatedAt {
		t.Fatal("child replaced original business acceptance instant")
	}
}

func TestBatchRemainingPhaseProtectionAndMeasuredReplay(t *testing.T) {
	s, p, parent, _ := batchCase(t, false)
	ctx := context.Background()
	left, _, enough := remainingBatchLimits(p, BatchUsage{ExecutionMillis: 20000, Phases: BatchPhaseUsage{RulesMillis: 20000}}, 2)
	if enough || left.Limits.RulesSeconds != 0 {
		t.Fatal("phase zero refilled", left)
	}
	child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
	if e != nil || !found {
		t.Fatal(e)
	}
	finishBatchAudit(t, s, child, 0, 0, false)
	if e = s.RecordBatchPhaseUsage(ctx, child.ID, BatchPhaseUsage{}); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordBatchPhaseUsage(ctx, child.ID, BatchPhaseUsage{}); e != nil {
		t.Fatal("same normalized phase measurement lost idempotency", e)
	}
	seg, e := s.RefreshBatchSegment(ctx, parent.ID, 1)
	if e != nil || seg.PhaseSource != "measured" {
		t.Fatal(seg, e)
	}
	if seg.ExecutionMillis != seg.Phases.CollectionMillis+seg.Phases.RulesMillis+seg.Phases.ModelMillis+seg.Phases.FinalizeMillis {
		t.Fatal("unaccounted segment wall time")
	}
	second, found, e := s.StartNextBatchSegment(ctx, parent.ID)
	if e != nil || !found {
		t.Fatal(e)
	}
	if seg.ExecutionMillis > 0 && second.Workload.Limits.FinalizeSeconds >= p.Workload.Limits.FinalizeSeconds {
		t.Fatal("measured finalize time refilled")
	}
}
func TestBatchRootCancellationAndRecoveryKeepUnprocessedObjects(t *testing.T) {
	for _, action := range []string{"cancel", "recover"} {
		t.Run(action, func(t *testing.T) {
			s, _, parent, _ := batchCase(t, false)
			ctx := context.Background()
			child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
			if e != nil || !found {
				t.Fatal(e)
			}
			if action == "cancel" {
				if _, e = s.Cancel(ctx, parent.ID); e != nil {
					t.Fatal(e)
				}
				if _, _, e = s.StartNextBatchSegment(ctx, parent.ID); !errors.Is(e, ErrJobTerminal) {
					t.Fatal("cancelled root started another segment", e)
				}
				e = s.FinishWithCode(ctx, parent.ID, "partial", "REQUEST_CANCELLED")
			} else {
				e = s.Recover(ctx)
			}
			if e != nil {
				t.Fatal("close root and internal work", e)
			}
			current, e := s.Get(ctx, child.ID)
			if e != nil || !terminal(current.Status) {
				t.Fatal("internal work remained active", current, e)
			}
			value, e := s.GetBatchRequest(ctx, parent.ID)
			if e != nil || len(value.Unprocessed) != 3 || len(value.Failed) != 2 {
				t.Fatal("lost failed/unprocessed finite coverage", value, e)
			}
			var queued int64
			s.db.Raw("SELECT count(*) FROM e0_jobs WHERE parent_job_id IS NOT NULL AND status='queued'").Scan(&queued)
			if queued != 0 {
				t.Fatal("recovery enqueued an internal replay")
			}
		})
	}
}

func TestBatchInternalAuthorityRejectsSelectionOutsideItsFixedSegment(t *testing.T) {
	s, _, parent, in := batchCase(t, false)
	ctx := context.Background()
	child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
	if e != nil || !found {
		t.Fatal(e)
	}
	cp, e := s.GetPlan(ctx, child.PlanID)
	if e != nil {
		t.Fatal(e)
	}
	cp.PreparedInput.Assets[0] = in.Assets[4]
	cp.PreparedInput.Selection.AssetIDs[0] = in.Assets[4].ID
	raw, _ := json.Marshal(cp.PreparedInput)
	if e = s.db.Exec("UPDATE execution_plans SET prepared_json=? WHERE id=?", string(raw), cp.ID).Error; e != nil {
		t.Fatal(e)
	}
	e = s.db.Transaction(func(tx *gorm.DB) error { _, err := batchAuthority(tx, child); return err })
	if !errors.Is(e, ErrInputInvalid) {
		t.Fatal("child expanded its accepted segment", e)
	}
}

func TestBatchPinnedRetainsExistingOfflineIdentityGrammar(t *testing.T) {
	for _, kind := range []string{"object", "scope", "both"} {
		t.Run(kind, func(t *testing.T) {
			s, e := Open(filepath.Join(t.TempDir(), "offline-identities.sqlite"), true)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			ctx := context.Background()
			in, e := audit.SyntheticExample("internet-in")
			if e != nil {
				t.Fatal(e)
			}
			if kind != "object" {
				setInputScopeRef(&in, "artifact:internet:in")
			}
			base := in.Assets[0]
			in.Assets = nil
			in.Selection.AssetIDs = nil
			resources := []PreparedResource{}
			for i := 0; i < 3; i++ {
				a := base
				a.ID = fmt.Sprintf("vm.web.%d", i)
				if kind == "scope" {
					a.ID = fmt.Sprintf("asset-%d", i)
				}
				in.Assets = append(in.Assets, a)
				in.Selection.AssetIDs = append(in.Selection.AssetIDs, a.ID)
				resources = append(resources, PreparedResource{AssetID: a.ID, ResourceType: "ecs"})
			}
			if e = audit.Validate(in); e != nil {
				t.Fatal(e)
			}
			snap, e := s.SaveSnapshot(ctx, in)
			if e != nil {
				t.Fatal(e)
			}
			frozen, heads, e := s.FreezeInput(ctx, snap.ID)
			if e != nil {
				t.Fatal(e)
			}
			conv, e := s.CreateConversation(ctx, "existing offline identity grammar", in.Scope.Ref)
			if e != nil {
				t.Fatal(e)
			}
			limits := workload.Default("audit")
			limits.AssetsPerBatch = 2
			p, e := s.SavePlan(ctx, ExecutionPlan{Kind: "audit", ConversationID: conv.ID, ConversationRevision: conv.Revision, DataSourceID: "local-offline", PolicyScopeRef: in.Scope.Ref, SnapshotID: snap.ID, Mode: "rules_only", ConfigRevision: "offline-identity-batch", PreparedInput: frozen, Heads: heads, Workload: workload.Resolved{ProfileRef: "offline-identity-batch", Limits: limits}, Batch: &BatchSpec{Resources: resources, Size: 2}})
			if e != nil {
				t.Fatal("supported offline identity rejected only by batching", e)
			}
			parent, _, e := s.EnqueuePlan(ctx, p.ID, "accept-offline-identities")
			if e != nil {
				t.Fatal(e)
			}
			parent, found, e := s.Claim(ctx)
			if e != nil || !found {
				t.Fatal(e)
			}
			child, found, e := s.StartNextBatchSegment(ctx, parent.ID)
			if e != nil || !found {
				t.Fatal(e)
			}
			cp, e := s.GetPlan(ctx, child.PlanID)
			if e != nil || cp.PreparedInput.Scope != in.Scope || cp.PreparedInput.Assets[0].ID != in.Assets[0].ID {
				t.Fatal("offline identities changed", e)
			}
		})
	}
	// Cloud configuration still uses the provider grammar even if the same
	// string is a supported identifier in an explicitly fixed local snapshot.
	limits := workload.Default("audit")
	limits.AssetsPerBatch = 2
	in, _ := audit.SyntheticExample("internet-in")
	p := ExecutionPlan{Kind: "audit", DataSourceID: "cloud", PolicyScopeRef: in.Scope.Ref, PreparedInput: domain.Input{Scope: in.Scope}, Preparation: &PreparationSpec{}, Workload: workload.Resolved{ProfileRef: "cloud-identity-batch", Limits: limits}, Batch: &BatchSpec{Size: 2, Resources: []PreparedResource{{ResourceID: "i.bad.0", AssetID: "i.bad.0", Region: "cn-test", ResourceType: "ecs"}, {ResourceID: "i-good-1", AssetID: "i-good-1", Region: "cn-test", ResourceType: "ecs"}, {ResourceID: "i-good-2", AssetID: "i-good-2", Region: "cn-test", ResourceType: "ecs"}}}}
	if _, e := batchSpec(p); !errors.Is(e, ErrInputInvalid) {
		t.Fatal("offline compatibility widened native cloud identifiers", e)
	}
}

package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"cfw-policy-llm-agent/server/internal/storage"
)

func planCase(t *testing.T) (*storage.Store, *PlanService, storage.SnapshotRecord, storage.Conversation, PlanRequest) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "plans.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	snapshot, err := NewAuditService(store).LoadExample(context.Background(), "internet-in")
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.CreateConversation(context.Background(), "人工业务审计", snapshot.Input.Scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	request := PlanRequest{Kind: "audit", ConversationID: conversation.ID, DataSourceID: "local-offline", PolicyScopeRef: snapshot.Input.Scope.Ref, Selection: PlanSelection{Kind: "assets", AssetIDs: append([]string(nil), snapshot.Input.Selection.AssetIDs...)}, DataPolicy: DataPolicy{Mode: "pinned_snapshot", SnapshotID: snapshot.ID}, BaselineVersionID: "B2-v1", Mode: "rules_only"}
	return store, NewPlanService(store), snapshot, conversation, request
}

func TestPlanPreviewFreezesLocalInputsWithoutStartingExecution(t *testing.T) {
	store, service, snapshot, conversation, request := planCase(t)
	preview, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ID == "" || preview.EffectCeiling.CloudHTTPAttempts != 0 || preview.EffectCeiling.ModelHTTPAttempts != 0 || preview.PreparedInput.SourceKind != "synthetic" {
		t.Fatal("preview changed effects or provenance")
	}
	jobs, err := store.ListJobs(context.Background(), conversation.ID, 0, 100)
	if err != nil || len(jobs) != 0 {
		t.Fatal("preview executed a task")
	}
	request.Selection.AssetIDs[0] = "outside"
	persisted, err := store.GetPlan(context.Background(), preview.ID)
	if err != nil || persisted.PreparedInput.Selection.AssetIDs[0] != snapshot.Input.Selection.AssetIDs[0] {
		t.Fatal("caller mutated accepted preview input")
	}
	if _, _, err = store.EnqueuePlan(context.Background(), preview.ID, "accept-original", service.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	original, replayed, err := store.EnqueuePlan(context.Background(), preview.ID, "accept-original", service.ConfigRevision)
	if err != nil || !replayed || original.ID == "" {
		t.Fatal("accepted execution lost idempotent identity")
	}
}

func TestPlanRequestCannotBroadenSourceScopeOrSelectedObjects(t *testing.T) {
	for _, mode := range []string{"scope", "source", "empty-assets", "duplicate-assets", "other-asset", "refresh", "baseline", "model-url-mode", "client-evaluation"} {
		t.Run(mode, func(t *testing.T) {
			_, service, _, _, request := planCase(t)
			switch mode {
			case "scope":
				request.PolicyScopeRef = "unknown-scope"
			case "source":
				request.DataSourceID = "machine-profile"
			case "empty-assets":
				request.Selection.AssetIDs = nil
			case "duplicate-assets":
				request.Selection.AssetIDs = append(request.Selection.AssetIDs, request.Selection.AssetIDs[0])
			case "other-asset":
				request.Selection.AssetIDs[0] = "other"
			case "refresh":
				request.DataPolicy.Mode = "force_refresh"
			case "baseline":
				request.BaselineVersionID = "model-proposed-policy"
			case "model-url-mode":
				request.Mode = "arbitrary-url"
			case "client-evaluation":
				request.EvaluationAt = "2020-01-01T00:00:00Z"
			}
			if _, err := service.Preview(context.Background(), request); err == nil {
				t.Fatal("client broadened plan authority")
			}
		})
	}
}

func TestUnconfiguredAgentAuditKeepsRulesAndHasNoModelAllowance(t *testing.T) {
	_, service, _, _, request := planCase(t)
	request.Mode = "rules_and_agent"
	plan, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.EffectCeiling.AllowModelRequests || plan.EffectCeiling.ModelHTTPAttempts != 0 || len(plan.Gaps) == 0 {
		t.Fatal("unconfigured model preview promised authorized requests")
	}
}

func TestAssistantUnconfiguredCannotCreateAnOrphanMessage(t *testing.T) {
	store, service, snapshot, conversation, _ := planCase(t)
	_, _, _, err := service.StartAssistant(context.Background(), nil, conversation.ID, AssistantRequest{ClientMessageID: "message-1", PolicyScopeRef: snapshot.Input.Scope.Ref, SnapshotID: snapshot.ID, Text: "解释当前规则", ModelUseAccepted: true})
	if !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("unexpected assistant state: %v", err)
	}
	page, err := store.ConversationMessages(context.Background(), conversation.ID, 0, 100)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("message was saved without accepted execution")
	}
}

func TestPlanUsesExplicitCurrentBusinessVersions(t *testing.T) {
	store, service, snapshot, _, request := planCase(t)
	initial, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := store.CreateProfileDraft(context.Background(), storage.CreateDraftRequest{SnapshotID: snapshot.ID, AssetIDs: snapshot.Input.Selection.AssetIDs})
	if err != nil {
		t.Fatal(err)
	}
	change := draft.Changes[0]
	change.Roles[0].Role = "unknown"
	draft, err = store.UpdateProfileDraft(context.Background(), draft.ID, storage.UpdateDraftRequest{ExpectedDraftRevision: draft.Revision, Changes: []storage.AssetDraftChange{change}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.PreviewProfileDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ConfirmProfileDraft(context.Background(), draft.ID, storage.ConfirmDraftRequest{PreviewID: preview.ID, ExpectedDraftRevision: draft.Revision, ConfirmedSections: []string{"roles"}}, "confirm-role")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.EnqueuePlan(context.Background(), initial.ID, "stale-plan", service.ConfigRevision); !errors.Is(err, storage.ErrPlanStale) {
		t.Fatal("changed business head did not invalidate unconsumed plan", err)
	}
	latest, err := service.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if latest.PreparedInput.ProfileVersion == initial.PreparedInput.ProfileVersion {
		t.Fatal("confirmed role version not frozen in plan")
	}
	encoded, _ := json.Marshal(latest)
	if string(encoded) == "" {
		t.Fatal("missing public preview")
	}
}

func TestE3PrivateModelAttemptLimitIsFrozenInEveryAcceptedIntent(t *testing.T) {
	for _, limit := range []int{1, 3, 6} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			store, service, snapshot, conversation, request := planCase(t)
			service.ModelReady = true
			service.ModelHTTPAttempts = limit
			request.Mode = "rules_and_agent"
			preview, err := service.Preview(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if preview.EffectCeiling.ModelHTTPAttempts != limit {
				t.Fatal("audit expanded configured physical-request allowance")
			}
			engine := New(store, nil)
			defer engine.Stop()
			job, _, _, err := service.StartAssistant(context.Background(), engine, conversation.ID, AssistantRequest{ClientMessageID: "e3-capped-turn", PolicyScopeRef: snapshot.ScopeRef, SnapshotID: snapshot.ID, Text: "解释固定资料", ModelUseAccepted: true})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := store.GetPlan(context.Background(), job.PlanID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.EffectCeiling.ModelHTTPAttempts != min(limit, 4) {
				t.Fatal("assistant expanded its or the configured allowance")
			}
		})
	}
}

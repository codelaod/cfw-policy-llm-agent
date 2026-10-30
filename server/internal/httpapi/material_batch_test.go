package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/storage"
)

// Material can contain the whole selected set; the accepted execution must
// still respect its segment protection instead of truncating the material.
func TestImportWholeMaterialThenExplicitBatchPreviewPreservesAllObjects(t *testing.T) {
	h := newWorkflowHarness(t)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	base := in.Assets[0]
	base.Services, base.Allowed = []domain.Service{}, []domain.Access{}
	base.AllowedConfirmed, base.AllowedComplete = false, false
	in.Assets, in.Selection.AssetIDs = []domain.Asset{}, []string{}
	words := []string{"检查杭州 ECS"}
	for i := 0; i < 51; i++ {
		a := base
		a.ID, a.ProviderResourceID = fmt.Sprintf("asset-material-%02d", i), fmt.Sprintf("i-material-%02d", i)
		a.Name, a.Region, a.ResourceType = fmt.Sprintf("material-%02d", i), "cn-hangzhou", "ecs"
		a.Address = fmt.Sprintf("192.0.2.%d", i+1)
		in.Assets = append(in.Assets, a)
		in.Selection.AssetIDs = append(in.Selection.AssetIDs, a.ID)
		words = append(words, a.ProviderResourceID)
	}
	encoded, _ := json.Marshal(in)
	status, body := h.request(t, "POST", "/api/v1/snapshots/import", string(encoded))
	if status != 201 {
		t.Fatalf("whole material incorrectly limited to execution segment: %d %s", status, body)
	}
	var snapshot storage.SnapshotRecord
	if err = json.Unmarshal(body, &snapshot); err != nil || len(snapshot.Input.Assets) != 51 || snapshot.SourceKind != "imported" {
		t.Fatal("import lost objects or forged provenance", err)
	}
	conv, err := h.engine.Store.CreateConversation(context.Background(), "完整人工集合", "")
	if err != nil {
		t.Fatal(err)
	}
	postTurn := func(key, text string) httpapi.IntentResponse {
		t.Helper()
		b, _ := json.Marshal(app.IntentTurnRequest{ClientTurnID: key, Text: text})
		status, body := h.keyed(t, "/api/v1/conversations/"+conv.ID+"/intent-turns", string(b), key)
		var turn httpapi.IntentResponse
		if err := json.Unmarshal(body, &turn); err != nil || status != 200 || turn.Intent == nil {
			t.Fatal("intent route failed", status, err, string(body))
		}
		return turn
	}
	need := postTurn("whole-selection", strings.Join(words, " "))
	if need.Intent.Status != "needs_batch" || len(need.Intent.Selection) != 51 || need.Proposal != nil && need.Proposal.PlanID != "" {
		t.Fatal("single execution accepted/truncated whole material", need.Intent.Status)
	}
	offer := postTurn("whole-batch", "按完整对象集分批审计")
	if offer.Proposal == nil || offer.Proposal.PlanID == "" {
		t.Fatal("full material has no batch preview", offer.Intent.Status)
	}
	p, err := h.engine.Store.GetPlan(context.Background(), offer.Proposal.PlanID)
	if err != nil || p.Batch == nil || len(p.Batch.Resources) != 51 || p.Batch.Size != 50 || len(p.PreparedInput.Assets) != 51 {
		t.Fatal("preview lost full set or changed segment policy", err)
	}
	jobs, err := h.engine.Store.ListJobs(context.Background(), conv.ID, 0, 100)
	if err != nil || len(jobs) != 0 {
		t.Fatal("import or preview executed work", err)
	}
}

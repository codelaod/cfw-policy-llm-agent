//go:build e3browserfixture

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestHistoricalGuidanceBrowserServe(t *testing.T) {
	root, e := filepath.Abs(filepath.Join("..", "..", ".."))
	if e != nil {
		t.Fatal(e)
	}
	statePath := filepath.Join(root, "runtime", "guidance-browser-service.json")
	stopPath := filepath.Join(root, "runtime", "guidance-browser-stop")
	if _, e = os.Stat(statePath); !os.IsNotExist(e) {
		t.Fatal("resolve the previous explicit guidance fixture first")
	}
	f := startE3BrowserFixture(t, t.TempDir())
	ctx := context.Background()
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	in.SnapshotVersion = "public-guidance-a"
	in.Assets[0].Name, in.Assets[0].Region, in.Assets[0].ResourceType, in.Assets[0].ProviderResourceID = "guidance-a", "cn-hangzhou", "ecs", "i-guidance-a"
	a, e := f.store.SaveSnapshot(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(in)
	var bInput domain.Input
	_ = json.Unmarshal(encoded, &bInput)
	bInput.SnapshotVersion, bInput.Assets[0].ID = "public-guidance-b", "asset-guidance-b"
	bInput.Assets[0].Name, bInput.Assets[0].Region, bInput.Assets[0].ProviderResourceID = "guidance-b", "cn-beijing", "i-guidance-b"
	bInput.Selection.AssetIDs = []string{bInput.Assets[0].ID}
	b, e := f.store.SaveSnapshot(ctx, bInput)
	if e != nil {
		t.Fatal(e)
	}
	c, e := f.store.CreateConversation(ctx, "公开历史A与当前B", a.ScopeRef)
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.plans.Preview(ctx, app.PlanRequest{Kind: "audit", ConversationID: c.ID, DataSourceID: "local-offline", PolicyScopeRef: a.ScopeRef, Selection: app.PlanSelection{Kind: "assets", AssetIDs: in.Selection.AssetIDs}, DataPolicy: app.DataPolicy{Mode: "pinned_snapshot", SnapshotID: a.ID}, BaselineVersionID: audit.ProjectBaseline().Version, Mode: "rules_only"})
	if e != nil {
		t.Fatal(e)
	}
	j, _, e := f.engine.EnqueuePlan(ctx, p.ID, "guidance-browser-rules-a", f.plans.ConfigRevision)
	if e != nil {
		t.Fatal(e)
	}
	var view storage.JobWorkspace
	for until := time.Now().Add(10 * time.Second); time.Now().Before(until); {
		view, e = f.store.ReadJobWorkspace(ctx, j.ID)
		if e != nil {
			t.Fatal(e)
		}
		if storage.Terminal(view.Job.Status) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if view.Job.Status != "completed" || view.ReportRef == "" {
		t.Fatal("public rules report A unavailable")
	}
	f.request(t, "POST", "/api/v1/conversations/"+c.ID+"/intent-turns", app.IntentTurnRequest{ClientTurnID: "guidance-browser-current-b", Text: "检查北京 ECS i-guidance-b，使用模型补充"}, "guidance-browser-current-b")
	usage, e := f.store.BudgetUsage(ctx, f.plans.BudgetRef, "", "")
	if e != nil || usage.ModelAttempts != 0 || usage.CloudAttempts != 0 {
		t.Fatal("fixture preparation made provider requests")
	}
	state, _ := json.Marshal(map[string]string{"base_url": f.api.URL, "application_token": f.token, "conversation_id": c.ID, "job_a": j.ID, "report_a": view.ReportRef, "snapshot_a": a.ID, "snapshot_b": b.ID, "budget_ref": f.plans.BudgetRef, "stop_path": stopPath, "source": "public artificial inputs; real local protocol adapter configured but no model/cloud request made"})
	if e = os.WriteFile(statePath, state, 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.Remove(statePath); os.Remove(stopPath) })
	fmt.Printf("Historical guidance local browser fixture ready at %s; state runtime/guidance-browser-service.json\n", f.api.URL)
	for until := time.Now().Add(20 * time.Minute); time.Now().Before(until); {
		if _, e = os.Stat(stopPath); e == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("explicit guidance browser fixture window expired")
}

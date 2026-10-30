package app

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestIdentityOnlyCheckRequiresNoPreconfiguredAssetsAndReadsNoProduct(t *testing.T) {
	var sends atomic.Int32
	st, plans, cloud, req := cloudIntegration(t, "internet", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		r.ParseForm()
		op := r.Header.Get("x-acs-action")
		if op == "" {
			op = r.Form.Get("Action")
		}
		if op != "GetCallerIdentity" {
			t.Error("identity-only plan read product data", op)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudProtocolFixture(op))
	}))
	for ref, scope := range cloud.Scopes {
		scope.AssetRegions = map[string]string{}
		cloud.Scopes[ref] = scope
	}
	req.Kind, req.Selection = "connection_check", PlanSelection{Kind: "identity_only"}
	ctx := context.Background()
	p, e := plans.Preview(ctx, req)
	if e != nil || sends.Load() != 0 || p.PreparedInput.Selection.Kind != "identity_only" {
		t.Fatal("identity preview required an unrelated configured resource", e)
	}
	if _, _, e = st.EnqueuePlan(ctx, p.ID, "identity-only-accept", plans.ConfigRevision); e != nil {
		t.Fatal(e)
	}
	j, found, e := st.Claim(ctx)
	if e != nil || !found {
		t.Fatal(e)
	}
	if e = cloud.Run(ctx, j); e != nil {
		t.Fatal(e)
	}
	view, e := st.ReadJobWorkspace(ctx, j.ID)
	if e != nil || sends.Load() != 1 || view.Job.Status != "partial" || view.CloudResult == nil || !view.CloudResult.Content.IdentityVerified || view.CloudResult.Content.ProductReady || view.SnapshotID != "" || view.InputSealed || view.Audit != nil {
		t.Fatal("identity-only check expanded into product readiness", e)
	}
	for _, bad := range []PlanSelection{{Kind: "identity_only", AssetIDs: []string{"i-runtime-synthetic"}}, {Kind: "identity_only", GatewayRef: "ngw-runtime-synthetic"}} {
		req.Selection = bad
		if _, e = plans.Preview(ctx, req); e == nil || sends.Load() != 1 {
			t.Fatal("identity-only effect accepted resource expansion", e)
		}
	}
}

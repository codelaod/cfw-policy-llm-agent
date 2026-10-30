package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/domain"
)

func TestIndependentIdentityOnlyIsRejectedByEveryCollectionAdmission(t *testing.T) {
	var sent atomic.Int32
	store, plans, cloud, req := cloudIntegration(t, "internet", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudProtocolFixture("GetCallerIdentity"))
	}))
	ctx := context.Background()
	req.Kind, req.Selection = "connection_check", PlanSelection{Kind: "identity_only"}
	check, err := plans.Preview(ctx, req)
	if err != nil || sent.Load() != 0 {
		t.Fatal("local identity-only preview unavailable", err)
	}
	req.Kind = "collect"
	if _, err = plans.Preview(ctx, req); err == nil || sent.Load() != 0 {
		t.Fatal("application promoted identity-only into collection", err)
	}
	p := check.ExecutionPlan
	p.ID, p.Kind = "", "collect"
	p.Workload, err = plans.ResolveWorkload("collect")
	if err != nil {
		t.Fatal(err)
	}
	p.EffectCeiling.CloudHTTPAttempts = p.Workload.Limits.CloudHTTPAttempts
	p.Request, _ = json.Marshal(req)
	if _, err = store.SavePlan(ctx, p); err == nil || sent.Load() != 0 {
		t.Fatal("storage accepted identity-only collection plan", err)
	}
	var cfg aliyuncollector.Config
	for _, candidate := range cloud.Scopes {
		cfg = candidate
	}
	reader := &cloudReaderFixture{}
	collector, err := aliyuncollector.New(reader, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = collector.Collect(ctx, domain.Selection{Kind: "identity_only"}); err == nil || reader.reads != 0 || sent.Load() != 0 {
		t.Fatal("collector executed identity-only as a product selector", err)
	}
}

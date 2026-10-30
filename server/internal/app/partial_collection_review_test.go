package app

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

// This is an independent product regression through the real signed SDK,
// manager and local ledger. The endpoint supplies artificial public responses.
func TestIndependentPartialCollectionPreservesObservedEvidenceAfterFirstACLFailure(t *testing.T) {
	var physical atomic.Int32
	store, plans, cloud, request := cloudIntegration(t, "internet", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		_ = r.ParseForm()
		action := r.Header.Get("x-acs-action")
		if action == "" {
			action = r.Form.Get("Action")
		}
		w.Header().Set("Content-Type", "application/json")
		if action == "DescribeControlPolicy" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"Code":"Forbidden","Message":"public artificial ACL permission failure"}`)
			return
		}
		_, _ = io.WriteString(w, cloudProtocolFixture(action))
	}))
	ctx := context.Background()
	offer, err := plans.Preview(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.EnqueuePlan(ctx, offer.ID, "independent-partial-accept", plans.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	job, found, err := store.Claim(ctx)
	if err != nil || !found {
		t.Fatal(err)
	}
	w := NewWorkflow(store)
	w.Cloud = cloud
	err = w.Run(ctx, job)
	if err != nil || physical.Load() != 5 {
		t.Fatal("fixture did not stop after four identity/product observations", err, physical.Load())
	}
	view, err := store.ReadJobWorkspace(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.CloudResult == nil || len(view.CloudResult.Content.Observations) == 0 {
		t.Fatalf("first ACL permission failure discarded already read ECS/CFW/SG observations: sends=%d cloud_result=%v input_sealed=%t", physical.Load(), view.CloudResult, view.InputSealed)
	}
	if view.Job.Status != "partial" || !view.InputSealed || view.CloudResult.Content.SnapshotID == "" {
		t.Fatal("verified first-pass evidence was not preserved as explicitly partial", view.CloudResult.Content)
	}
	if view.CloudResult.Content.ProductReady && len(view.CloudResult.Content.Limitations) == 0 {
		t.Fatal("failed collection claimed complete product readiness", view.CloudResult.Content)
	}
	input, err := store.GetJobInput(ctx, job.ID)
	if err != nil || input.Completeness.EnumerationComplete || input.Completeness.ObservationConsistency != "unverified" || len(input.Completeness.KnownGaps) == 0 || len(input.Assets) != 1 {
		t.Fatal("partial input lost failed-family limitations", err)
	}
}

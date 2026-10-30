package app

import (
	"cfw-policy-llm-agent/server/internal/storage"
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestIndependentLateSuccessfulResponseAfterCancelKeepsOnlyFailureObservations(t *testing.T) {
	var store *storage.Store
	var jobID string
	var acl atomic.Int32
	st, plans, cloud, req := cloudIntegration(t, "internet", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		op := r.Header.Get("x-acs-action")
		if op == "" {
			op = r.Form.Get("Action")
		}
		if op == "DescribeControlPolicy" && acl.Add(1) == 2 {
			if _, err := store.Cancel(context.Background(), jobID); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cloudProtocolFixture(op))
	}))
	store = st
	ctx := context.Background()
	offered, err := plans.Preview(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.EnqueuePlan(ctx, offered.ID, "late-cancel-observations", plans.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	job, found, err := st.Claim(ctx)
	if err != nil || !found {
		t.Fatal(err)
	}
	jobID = job.ID
	w := NewWorkflow(st)
	w.Cloud = cloud
	if err = w.Run(ctx, job); err == nil {
		t.Fatal("cancelled producer claimed success")
	}
	view, err := st.ReadJobWorkspace(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.InputSealed || view.SnapshotID != "" || view.Audit != nil {
		t.Fatal("late success published a cancelled input")
	}
	if view.CloudResult == nil || view.CloudResult.Content.ProductReady || view.CloudResult.Content.SnapshotID != "" || len(view.CloudResult.Content.Observations) == 0 {
		t.Fatalf("safe observed pages were discarded after late response hit cancellation admission: status=%s cloud_result=%v", view.Job.Status, view.CloudResult)
	}
}

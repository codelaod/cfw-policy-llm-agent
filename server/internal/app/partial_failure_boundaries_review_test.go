package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIndependentPartialFailureBoundariesCannotSealUnsafeOrInterruptedData(t *testing.T) {
	for _, kind := range []string{"no_metadata", "foreign_owner", "malformed_acl", "cancelled", "secret_prefix"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var physical atomic.Int32
			const privateValue = "synthetic-private-observation-boundary"
			store, plans, cloud, request := cloudIntegration(t, "internet", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				physical.Add(1)
				_ = r.ParseForm()
				action := r.Header.Get("x-acs-action")
				if action == "" {
					action = r.Form.Get("Action")
				}
				w.Header().Set("Content-Type", "application/json")
				deny := kind == "no_metadata" && action == "DescribeInstances" || kind == "secret_prefix" && action == "DescribeAssetList"
				if deny {
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"Code":"Forbidden","Message":"public artificial permission failure"}`)
					return
				}
				if kind == "cancelled" && action == "DescribeControlPolicy" {
					cancel()
				}
				if kind == "malformed_acl" && action == "DescribeControlPolicy" {
					_, _ = io.WriteString(w, `[]`)
					return
				}
				body := cloudProtocolFixture(action)
				if kind == "foreign_owner" && action == "DescribeAssetList" {
					body = strings.Replace(body, "1234567890123456", "9999999999999999", 1)
				}
				if kind == "secret_prefix" && action == "DescribeInstances" {
					var value map[string]any
					_ = json.Unmarshal([]byte(body), &value)
					value["Instances"].(map[string]any)["Instance"].([]any)[0].(map[string]any)["InstanceName"] = privateValue
					encoded, _ := json.Marshal(value)
					body = string(encoded)
				}
				_, _ = io.WriteString(w, body)
			}))
			cloud.SensitiveValues = []string{privateValue}
			offer, err := plans.Preview(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = store.EnqueuePlan(ctx, offer.ID, "independent-boundary-accept", plans.ConfigRevision); err != nil {
				t.Fatal(err)
			}
			job, found, err := store.Claim(ctx)
			if err != nil || !found {
				t.Fatal(err)
			}
			workflow := NewWorkflow(store)
			workflow.Cloud = cloud
			err = workflow.Run(ctx, job)
			if err == nil {
				t.Fatal("unsafe or interrupted fixture unexpectedly completed")
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("task cancellation lost its original cause", err)
			}
			view, err := store.ReadJobWorkspace(context.Background(), job.ID)
			if err != nil || view.InputSealed || view.Audit != nil || view.SnapshotID != "" {
				t.Fatal("failure was promoted to a sealed audit input", err, view.Job)
			}
			if kind == "secret_prefix" {
				if view.CloudResult != nil {
					t.Fatal("credential-bearing prefix entered failed observation storage")
				}
			} else if view.CloudResult == nil || view.CloudResult.Content.ProductReady || view.CloudResult.Content.SnapshotID != "" {
				t.Fatal("already-read safe observation receipt missing or promoted", view.CloudResult)
			}
			before := physical.Load()
			if _, err = store.ReadJobWorkspace(context.Background(), job.ID); err != nil || before != physical.Load() {
				t.Fatal("observing failure restarted a provider request", err)
			}
			messages, err := store.ConversationMessages(context.Background(), job.ConversationID, 0, 100)
			encoded, _ := json.Marshal(messages)
			if err != nil || strings.Contains(string(encoded), privateValue) {
				t.Fatal("private prefix entered public messages", err)
			}
		})
	}
}

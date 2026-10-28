package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cfw-policy-llm-agent/server/internal/aliyunapi"
	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/outbound"
)

func TestReviewCloudProtocolRejectsKnownApplicationSecret(t *testing.T) {
	for _, test := range []struct {
		field, secret string
		value         any
	}{
		{"Description", "synthetic-app-token", "synthetic-app-token"},
		{"AclUuid", "synthetic-app-token", "synthetic-app-token"},
		{"FutureMatch", "3141592653589793", json.Number("3141592653589793")},
	} {
		t.Run(test.field, func(t *testing.T) {
			secret := test.secret
			var physical atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				physical.Add(1)
				_ = r.ParseForm()
				action := r.Header.Get("x-acs-action")
				if action == "" {
					action = r.Form.Get("Action")
				}
				body := cloudProtocolFixture(action)
				if action == "DescribeNatFirewallControlPolicy" {
					var envelope map[string]any
					if json.Unmarshal([]byte(body), &envelope) != nil {
						t.Fatal("invalid fixture")
					}
					envelope["Policys"].([]any)[0].(map[string]any)[test.field] = test.value
					encoded, _ := json.Marshal(envelope)
					body = string(encoded)
					if _, isString := test.value.(string); isString {
						body = strings.ReplaceAll(body, secret, `synthetic\u002dapp\u002dtoken`)
					}
				}
				_, _ = io.WriteString(w, body)
			})
			store, plans, cloud, req := cloudIntegration(t, "nat", handler)
			// This explicitly injects the already known application secret into
			// the protocol adapter, as the production runtime must do at startup.
			upstream := httptest.NewServer(handler)
			defer upstream.Close()
			cfg := cloudRuntimeConfig(t, true, plans.BudgetRef, 0)
			sources := aliyunapi.Sources(cfg)
			for i := range sources {
				sources[i].Origin = upstream.URL
				sources[i].AllowLoopbackHTTP = true
			}
			manager, err := outbound.NewManager(store, outbound.Config{Revision: plans.ConfigRevision, Sources: sources})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			factory, err := aliyunapi.New(manager, aliyunapi.Config{Connection: cfg, SensitiveValues: []string{secret}, FixtureOrigin: upstream.URL, AllowLoopbackFixture: true})
			if err != nil {
				t.Fatal(err)
			}
			cloud.NewReader = func(ctx context.Context, id string) (CloudReader, error) { return factory.Reader(ctx, id) }
			preview, err := plans.Preview(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = store.EnqueuePlan(context.Background(), preview.ID, "reject-protocol-secret", plans.ConfigRevision); err != nil {
				t.Fatal(err)
			}
			job, ok, err := store.Claim(context.Background())
			if err != nil || !ok {
				t.Fatal(err)
			}
			err = cloud.Run(context.Background(), job)
			if err == nil || !strings.Contains(err.Error(), "CLOUD_RESPONSE_CONTAINS_CREDENTIAL") || strings.Contains(err.Error(), secret) {
				t.Fatal("known application credential passed the cloud protocol boundary", err)
			}
			if physical.Load() != 3 {
				t.Fatal("protocol reflection was only caught after more provider reads", physical.Load())
			}
			view, err := store.ReadJobWorkspace(context.Background(), job.ID)
			if err != nil || view.InputSealed || view.CloudResult != nil {
				t.Fatal("credential-bearing provider data reached persisted business results", err)
			}
		})
	}
}

func TestReviewCloudPersistenceRechecksInputAndEscapedPrivateArtifacts(t *testing.T) {
	for _, location := range []string{"input_id", "input_condition", "raw_value", "raw_key", "raw_filter", "input_number"} {
		t.Run(location, func(t *testing.T) {
			secret := "synthetic-app-token"
			if location == "input_number" {
				secret = "3141592653589793"
			}
			store, plans, cloud, _, req, input := cloudPlanCase(t)
			cloud.SensitiveValues = []string{secret}
			before, err := store.ListSnapshots(context.Background(), 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			baseCollect := cloud.collect
			cloud.collect = func(ctx context.Context, reader CloudReader, cfg aliyuncollector.Config, selection domain.Selection) (aliyuncollector.Collection, error) {
				collected, err := baseCollect(ctx, reader, cfg, selection)
				if err != nil {
					return collected, err
				}
				collected.Input = input
				body := json.RawMessage(`{"Description":"synthetic-private-observation"}`)
				query := map[string]string{"Direction": "in"}
				switch location {
				case "input_id":
					collected.Input.Policies[0].ID = secret
				case "input_condition":
					collected.Input.Policies[0].Conditions = map[string]json.RawMessage{"external_note": json.RawMessage(`"synthetic\u002dapp\u002dtoken"`)}
				case "input_number":
					collected.Input.Policies[0].Conditions = map[string]json.RawMessage{"future_condition": json.RawMessage(secret)}
				case "raw_value":
					body = json.RawMessage(`{"Description":"synthetic\u002dapp\u002dtoken"}`)
				case "raw_key":
					body = json.RawMessage(`{"synthetic\u002dapp\u002dtoken":"synthetic"}`)
				case "raw_filter":
					query["synthetic"] = secret
				}
				digest := sha256.Sum256(body)
				collected.Artifacts = []aliyuncollector.RawArtifact{{Round: 1, Operation: "DescribeControlPolicy", Query: query, Body: body, SHA256: hex.EncodeToString(digest[:])}}
				return collected, nil
			}
			preview, err := plans.Preview(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = store.EnqueuePlan(context.Background(), preview.ID, "reject-persistence-secret", plans.ConfigRevision); err != nil {
				t.Fatal(err)
			}
			job, ok, err := store.Claim(context.Background())
			if err != nil || !ok {
				t.Fatal(err)
			}
			err = cloud.Run(context.Background(), job)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatal("known application credential passed the final cloud persistence boundary", err)
			}
			view, err := store.ReadJobWorkspace(context.Background(), job.ID)
			if err != nil || view.InputSealed || view.CloudResult != nil {
				t.Fatal("credential-bearing input or raw artifact was persisted", err)
			}
			after, err := store.ListSnapshots(context.Background(), 0, 100)
			if err != nil || len(after) != len(before) {
				t.Fatal("rejected cloud credentials created a snapshot", err)
			}
		})
	}
}

func TestReviewRuntimePropagatesKnownApplicationSecretToCloudBoundary(t *testing.T) {
	store, _, _, _, _ := planCase(t)
	model := runtimeConfig(t, true)
	cloud := cloudRuntimeConfig(t, true, model.Budget().Ref, 4)
	const secret = "synthetic-runtime-app-token"
	runtime, err := NewRuntime(store, model, cloud, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	contains := func(values []string, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	if runtime.Cloud == nil || !contains(runtime.Cloud.SensitiveValues, secret) || !contains(runtime.SensitiveValues, secret) {
		t.Fatal("known application token did not reach cloud and public projection boundaries")
	}
	if contains(runtime.Cloud.SensitiveValues, cloud.ExpectedAccountID()) {
		t.Fatal("expected account was confused with a raw-response credential reflection")
	}
}

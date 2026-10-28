package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

type cloudReaderFixture struct {
	verified, reads int
	identityErr     error
}

func (f *cloudReaderFixture) VerifyIdentity(context.Context) error {
	f.verified++
	return f.identityErr
}
func (f *cloudReaderFixture) Read(context.Context, string, map[string]string) (json.RawMessage, error) {
	f.reads++
	return json.RawMessage(`{}`), nil
}

func cloudPlanCase(t *testing.T) (*storage.Store, *PlanService, *CloudService, *cloudReaderFixture, PlanRequest, domain.Input) {
	t.Helper()
	store, plans, snap, conv, _ := planCase(t)
	in := snap.Input
	in.SourceKind = "live_readonly"
	in.Completeness.ObservationConsistency = "observed_stable"
	in.Scope.Ref = "scope-configured-cloud"
	in.Scope.AccountRef = "account-configured-cloud"
	in.Scope.ControlPlaneRef = "control-configured-cloud"
	in.Scope.PolicySetRef = "policy-configured-cloud"
	for i := range in.Assets {
		in.Assets[i].ScopeRef = in.Scope.Ref
		for j := range in.Assets[i].Services {
			for k := range in.Assets[i].Services[j].Allowed {
				in.Assets[i].Services[j].Allowed[k].ScopeRef = in.Scope.Ref
			}
			for k := range in.Assets[i].Services[j].Required {
				in.Assets[i].Services[j].Required[k].ScopeRef = in.Scope.Ref
			}
		}
		for j := range in.Assets[i].Allowed {
			in.Assets[i].Allowed[j].ScopeRef = in.Scope.Ref
		}
	}
	for i := range in.Policies {
		in.Policies[i].ScopeRef = in.Scope.Ref
	}
	if err := audit.Validate(in); err != nil {
		t.Fatal(err)
	}
	plans.ConfigRevision = "combined-cloud-config"
	plans.BudgetRef = "shared-cloud-period"
	reader := &cloudReaderFixture{}
	cloud := &CloudService{Store: store, SourceRef: "configured-cloud", ConfigRevision: plans.ConfigRevision, Configured: true, Ready: true, MaxHTTPAttempts: 20, Scopes: map[string]aliyuncollector.Config{in.Scope.Ref: {Scope: in.Scope, AssetRegions: map[string]string{in.Selection.AssetIDs[0]: "cn-hangzhou"}, MaxAttempts: 20}}, NewReader: func(context.Context, string) (CloudReader, error) { return reader, nil }}
	cloud.collect = func(context.Context, CloudReader, aliyuncollector.Config, domain.Selection) (aliyuncollector.Collection, error) {
		return aliyuncollector.Collection{Input: in, Observations: []aliyuncollector.Observation{{Round: 1, Operation: "DescribeControlPolicy", Pages: 1, StartedAt: in.CollectedAt, FinishedAt: in.CollectedAt, EnumerationComplete: true, SemanticSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}, nil
	}
	plans.Cloud = cloud
	if err := store.RegisterConfiguredScopes(context.Background(), cloud.SourceRef, plans.ConfigRevision, []domain.Scope{in.Scope}); err != nil {
		t.Fatal(err)
	}
	ref := in.Scope.Ref
	if _, err := store.PatchConversation(context.Background(), conv.ID, storage.ConversationPatch{ExpectedRevision: conv.Revision, PolicyScopeRef: &ref}); err != nil {
		t.Fatal(err)
	}
	request := PlanRequest{Kind: "collect", ConversationID: conv.ID, DataSourceID: cloud.SourceRef, PolicyScopeRef: in.Scope.Ref, Selection: PlanSelection{Kind: "assets", AssetIDs: append([]string(nil), in.Selection.AssetIDs...)}, DataPolicy: DataPolicy{Mode: "force_refresh"}, BaselineVersionID: audit.ProjectBaseline().Version, Mode: "rules_only"}
	return store, plans, cloud, reader, request, in
}

func TestCloudPlanPreviewIsLocalAndFreezesOnlyExplicitCollectionIntent(t *testing.T) {
	store, plans, _, reader, request, in := cloudPlanCase(t)
	p, err := plans.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if reader.verified != 0 || reader.reads != 0 || !p.EffectCeiling.AllowCloudReads || p.EffectCeiling.AllowModelRequests || p.EffectCeiling.CloudHTTPAttempts != 20 || p.SnapshotID != "" || !reflect.DeepEqual(p.PreparedInput.Scope, in.Scope) || !reflect.DeepEqual(p.PreparedInput.Selection, in.Selection) || len(p.PreparedInput.Assets) != 0 {
		t.Fatal("preview performed work or changed its frozen effects/scope")
	}
	jobs, err := store.ListJobs(context.Background(), request.ConversationID, 0, 100)
	if err != nil || len(jobs) != 0 {
		t.Fatal("preview started cloud task", err)
	}
	request.Selection.AssetIDs[0] = "other"
	if _, err = plans.Preview(context.Background(), request); !errors.Is(err, ErrScopeDenied) {
		t.Fatal("unconfigured object escaped the explicit allow set", err)
	}
}

func TestCloudPlanRejectsImplicitScopeEffectsAndSnapshotMixing(t *testing.T) {
	for _, kind := range []string{"empty", "duplicate", "gateway", "snapshot", "fallback", "model", "evaluation", "baseline", "source", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			_, plans, cloud, reader, req, _ := cloudPlanCase(t)
			switch kind {
			case "empty":
				req.Selection.AssetIDs = nil
			case "duplicate":
				req.Selection.AssetIDs = append(req.Selection.AssetIDs, req.Selection.AssetIDs[0])
			case "gateway":
				req.Selection = PlanSelection{Kind: "gateway_policy"}
			case "snapshot":
				req.DataPolicy.SnapshotID = "old"
			case "fallback":
				req.DataPolicy.AllowStaleFallback = true
			case "model":
				req.Mode = "rules_and_agent"
			case "evaluation":
				req.EvaluationAt = "2020-01-01T00:00:00Z"
			case "baseline":
				req.BaselineVersionID = "other"
			case "source":
				req.DataSourceID = "other-source"
			case "disabled":
				cloud.Ready = false
			}
			if _, err := plans.Preview(context.Background(), req); err == nil {
				t.Fatal("invalid cloud intent accepted")
			}
			if reader.verified != 0 || reader.reads != 0 {
				t.Fatal("invalid intent made a request")
			}
		})
	}
}

func TestAcceptedCloudCollectionFinalizesFixedSnapshotWithoutModelWork(t *testing.T) {
	store, plans, cloud, reader, request, _ := cloudPlanCase(t)
	p, err := plans.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := store.EnqueuePlan(context.Background(), p.ID, "accept-cloud", plans.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	j, ok, err := store.Claim(context.Background())
	if err != nil || !ok {
		t.Fatal(err)
	}
	w := NewWorkflow(store)
	w.Cloud = cloud
	if err = w.Run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	view, err := store.ReadJobWorkspace(context.Background(), j.ID)
	if err != nil || view.Job.Status != "completed" || view.CloudResult == nil || view.SnapshotID == "" || !view.InputSealed || view.Audit != nil || view.AgentResult != nil {
		t.Fatal("collection did not publish one atomic cloud result", err)
	}
	if reader.verified != 1 {
		t.Fatal("collection skipped explicit identity verification")
	}
	in, err := store.GetJobInput(context.Background(), j.ID)
	if err != nil || in.SourceKind != "live_readonly" {
		t.Fatal("collected input lost provenance", err)
	}
}

func TestCloudIdentityFailureStopsCollectionAndUsesControlledError(t *testing.T) {
	store, plans, cloud, reader, request, _ := cloudPlanCase(t)
	reader.identityErr = errors.New("private endpoint and identity body")
	called := false
	cloud.collect = func(context.Context, CloudReader, aliyuncollector.Config, domain.Selection) (aliyuncollector.Collection, error) {
		called = true
		return aliyuncollector.Collection{}, nil
	}
	p, err := plans.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.EnqueuePlan(context.Background(), p.ID, "identity-denied", plans.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	j, ok, err := store.Claim(context.Background())
	if err != nil || !ok {
		t.Fatal(err)
	}
	err = cloud.Run(context.Background(), j)
	var failure JobFailure
	if !errors.As(err, &failure) || failure.Code != "CLOUD_IDENTITY_FAILED" || called {
		t.Fatal("identity failure leaked details or continued product reads", err)
	}
	view, err := store.ReadJobWorkspace(context.Background(), j.ID)
	if err != nil || view.CloudResult != nil || view.InputSealed {
		t.Fatal("failed identity fabricated a snapshot", err)
	}
}

func TestCloudRunnerRejectsAnotherPreviewBeforeInitializingAReader(t *testing.T) {
	store, plans, cloud, reader, req, _ := cloudPlanCase(t)
	accepted, err := plans.Preview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.EnqueuePlan(context.Background(), accepted.ID, "cloud-authority", plans.ConfigRevision); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.Claim(context.Background())
	if err != nil || !ok {
		t.Fatal(err)
	}
	other, err := plans.Preview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	job.PlanID = other.ID
	err = cloud.Run(context.Background(), job)
	var failure JobFailure
	if !errors.As(err, &failure) || failure.Code != "CLOUD_PLAN_INVALID" || reader.verified != 0 || reader.reads != 0 {
		t.Fatal("unconsumed preview was used as execution authority", err, reader.verified)
	}
}

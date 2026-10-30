package app

import (
	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"context"
	"encoding/json"
	"testing"
)

type independentCollectionFatalReader struct {
	inner CloudReader
	code  string
}

func (r independentCollectionFatalReader) VerifyIdentity(ctx context.Context) error {
	return r.inner.VerifyIdentity(ctx)
}
func (r independentCollectionFatalReader) Read(ctx context.Context, op string, q map[string]string) (json.RawMessage, error) {
	if op == "DescribeAssetList" {
		return nil, aliyuncollector.Failure(r.code)
	}
	return r.inner.Read(ctx, op, q)
}
func TestIndependentFatalProductScopeFailureStopsFurtherBatchSegments(t *testing.T) {
	for _, code := range []string{"CLOUD_ASSET_SCOPE_MISMATCH", "CLOUD_GROUP_SCOPE_MISMATCH", "CLOUD_GATEWAY_MISMATCH", "CLOUD_RESPONSE_INVALID", "COLLECTOR_SCOPE_DENIED", "PROVIDER_RESPONSE_INVALID"} {
		t.Run(code, func(t *testing.T) {
			s, cloud, c, counts := batchSDKFixture(t)
			original := cloud.NewReader
			cloud.NewReader = func(ctx context.Context, id string) (CloudReader, error) {
				r, e := original(ctx, id)
				return independentCollectionFatalReader{inner: r, code: code}, e
			}
			offer := goalTurn(t, s, c.ID, "fatal-scope-offer", "分批检查杭州 ECS i-batch-public-1 i-batch-public-2 i-batch-public-3 i-batch-public-4 i-batch-public-5 入向，只运行规则")
			if offer.Proposal == nil || offer.Proposal.PlanID == "" {
				t.Fatal("fixture no full batch preview")
			}
			if _, e := s.Store.AcceptIntentProposal(context.Background(), c.ID, offer.Proposal.ID, "fatal-scope-accept", s.Plans.ConfigRevision, offer.Intent.Revision); e != nil {
				t.Fatal(e)
			}
			parent, found, e := s.Store.Claim(context.Background())
			if e != nil || !found {
				t.Fatal(e)
			}
			w := NewWorkflow(s.Store)
			w.Cloud = cloud
			if e = w.Run(context.Background(), parent); e == nil {
				t.Fatal("fatal fixture unexpectedly completed")
			}
			view, e := s.Store.ReadJobWorkspace(context.Background(), parent.ID)
			if e != nil || view.Batch == nil {
				t.Fatal(e)
			}
			if counts()["GetCallerIdentity"] != 1 || len(view.Batch.Unprocessed) != 3 || len(view.Batch.Segments[0].Resources) != 2 {
				t.Fatalf("fatal scope/identity response did not stop later segments: code=%s identities=%d covered=%d failed=%d unprocessed=%d", code, counts()["GetCallerIdentity"], len(view.Batch.Covered), len(view.Batch.Failed), len(view.Batch.Unprocessed))
			}
		})
	}
}

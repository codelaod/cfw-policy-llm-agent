package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cfw-policy-llm-agent/server/internal/aliyunapi"
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/outbound"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
)

// This exercises the real application, SDK signing and persistent ledger with
// artificial loopback responses. No provider account or performance claim.
func batchSDKFixture(t *testing.T, attemptLimit ...int) (*IntentService, *CloudService, storage.Conversation, func() map[string]int) {
	t.Helper()
	st, _, _, _, _ := planCase(t)
	t.Setenv("BATCH_PUBLIC_AK", "synthetic-batch-access-key")
	t.Setenv("BATCH_PUBLIC_SECRET", "synthetic-batch-access-secret")
	raw := `{"enabled":true,"connection_ref":"public-batch-cloud","credential_mode":"env_ak","access_key_id_env":"BATCH_PUBLIC_AK","access_key_secret_env":"BATCH_PUBLIC_SECRET","expected_account_id":"1234567890123456","control_plane_region":"cn-hangzhou","allowed_boundaries":["internet/in"],"allowed_regions":["cn-hangzhou"],"allowed_assets":[],"allowed_nat_gateways":[],"timeout_seconds":10,"max_http_attempts":100,"budget":{"ref":"public-batch-period","application_cloud_attempts":1000,"session_cloud_attempts":500,"application_model_attempts":0,"session_model_attempts":0}}`
	p := filepath.Join(t.TempDir(), "public-cloud.json")
	if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	cfg, e := cloudconfig.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	execution := batchDialogProfile(t, 20)
	if len(attemptLimit) > 0 {
		profiles := map[string]workload.Limits{}
		for _, kind := range []string{"audit", "assistant_turn", "collect", "connection_check"} {
			limits, err := execution.Profile(kind)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "audit" {
				limits.CloudHTTPAttempts = attemptLimit[0]
			}
			profiles[kind] = limits
		}
		encoded, _ := json.Marshal(map[string]any{"schema_version": 1, "profile_ref": "public-batch-total-cap", "profiles": profiles})
		policyPath := filepath.Join(t.TempDir(), "public-execution.json")
		if e = os.WriteFile(policyPath, encoded, 0600); e != nil {
			t.Fatal(e)
		}
		execution, e = workload.Load(policyPath)
		if e != nil {
			t.Fatal(e)
		}
	}
	runtime, e := NewRuntimeWithExecution(st, modelconfig.Config{}, cfg, execution)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(runtime.Close)
	var mu sync.Mutex
	counts := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		op := req.Header.Get("x-acs-action")
		if op == "" {
			op = req.Form.Get("Action")
		}
		mu.Lock()
		counts[op]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch op {
		case "DescribeInstances":
			var ids []string
			if json.Unmarshal([]byte(req.Form.Get("InstanceIds")), &ids) != nil || len(ids) == 0 || len(ids) > 2 {
				t.Error("accepted segment did not preserve explicit finite IDs")
			}
			rows := []any{}
			for _, id := range ids {
				rows = append(rows, map[string]any{"InstanceId": id, "InstanceName": id, "RegionId": "cn-hangzhou", "OSType": "linux", "SecurityGroupIds": map[string]any{"SecurityGroupId": []string{"sg-synthetic"}}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"NextToken": "", "Instances": map[string]any{"Instance": rows}, "TotalCount": len(rows)})
		case "DescribeAssetList":
			id := req.Form.Get("SearchItem")
			n, _ := strconv.Atoi(strings.TrimPrefix(id, "i-batch-public-"))
			if n < 1 || n > 5 {
				t.Error("CFW request escaped accepted set")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"TotalCount": 1, "Assets": []any{map[string]any{"BindInstanceId": id, "ResourceInstanceId": "eip-" + id, "RegionID": "cn-hangzhou", "AliUid": 1234567890123456, "InternetAddress": fmt.Sprintf("192.0.2.%d", n), "ProtectStatus": "open"}}})
		case "GetCallerIdentity", "DescribeSecurityGroupAttribute", "DescribeControlPolicy":
			_, _ = w.Write([]byte(cloudProtocolFixture(op)))
		default:
			t.Error("unexpected provider operation", op)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(up.Close)
	sources := aliyunapi.Sources(cfg)
	for i := range sources {
		sources[i].Origin, sources[i].AllowLoopbackHTTP = up.URL, true
	}
	manager, e := outbound.NewManager(st, outbound.Config{Revision: runtime.Plans.ConfigRevision, Sources: sources})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(manager.Close)
	factory, e := aliyunapi.New(manager, aliyunapi.Config{Connection: cfg, FixtureOrigin: up.URL, AllowLoopbackFixture: true})
	if e != nil {
		t.Fatal(e)
	}
	runtime.Cloud.NewReader = func(ctx context.Context, id string) (CloudReader, error) { return factory.Reader(ctx, id) }
	c, e := st.CreateConversation(context.Background(), "从未登记的完整ID集合", "")
	if e != nil {
		t.Fatal(e)
	}
	copyCounts := func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for op, n := range counts {
			out[op] = n
		}
		return out
	}
	return &IntentService{Store: st, Plans: runtime.Plans}, runtime.Cloud, c, copyCounts
}

func TestUnlistedIDsPrepareAndAuditWholeBatchThroughSignedSDKAndLedger(t *testing.T) {
	s, cloud, c, counts := batchSDKFixture(t)
	ctx := context.Background()
	ids := []string{}
	for i := 1; i <= 5; i++ {
		ids = append(ids, fmt.Sprintf("i-batch-public-%d", i))
	}
	first := goalTurn(t, s, c.ID, "unlisted-five", "检查杭州 ECS "+strings.Join(ids, " ")+" 入向，只运行规则")
	if first.Intent.Status != "needs_batch" || len(first.Intent.Selection) != 0 || len(first.Intent.Query.ResourceIDs) != 5 {
		t.Fatal("unknown IDs were truncated or manufactured as observed resources", first.Intent)
	}
	offer := goalTurn(t, s, c.ID, "unlisted-batch", "按完整对象集分批审计")
	if offer.Proposal == nil || offer.Proposal.PlanID == "" || !offer.Proposal.RequiresDiscovery || len(offer.Intent.Selection) != 0 || len(counts()) != 0 {
		t.Fatal("unlisted whole set not locally previewable", offer.Intent)
	}
	page, e := s.Store.SearchResources(ctx, storage.ResourceFilter{SourceRef: cloud.SourceRef}, "", 100)
	if e != nil || len(page.Items) != 0 {
		t.Fatal("preview manufactured directory observations", e)
	}
	a, e := s.Store.AcceptIntentProposal(ctx, c.ID, offer.Proposal.ID, "unlisted-accept", s.Plans.ConfigRevision, offer.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	parent, found, e := s.Store.Claim(ctx)
	if e != nil || !found || parent.ID != a.Job.ID {
		t.Fatal(e)
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	if e = w.Run(ctx, parent); e != nil {
		t.Fatal("actual SDK batch did not finish", e)
	}
	view, e := s.Store.ReadJobWorkspace(ctx, parent.ID)
	if e != nil || view.Job.Status != "completed" || view.Audit != nil || view.Batch == nil || len(view.Batch.Covered) != 5 || len(view.Batch.Segments) != 3 || len(view.Batch.Unprocessed) != 0 {
		t.Fatal("batch lost actual coverage or invented merged result", e, view.Job, view.Batch)
	}
	for _, seg := range view.Batch.Segments {
		if seg.Manifest == nil || seg.Manifest.SourceKind != "live_readonly" || seg.AuditRef == "" || seg.ReportRef == "" || seg.Status != "completed" {
			t.Fatal("segment missing fixed observed evidence", seg)
		}
	}
	want := map[string]int{"GetCallerIdentity": 3, "DescribeInstances": 6, "DescribeAssetList": 10, "DescribeSecurityGroupAttribute": 6, "DescribeControlPolicy": 6}
	for op, n := range want {
		if counts()[op] != n {
			t.Errorf("%s attempts=%d want=%d", op, counts()[op], n)
		}
	}
	usage, e := s.Store.BudgetUsage(ctx, s.Plans.BudgetRef, "", parent.ID)
	if e != nil || usage.CloudAttempts != 31 || view.Batch.Usage.CloudAttempts != 31 || usage.ModelAttempts != 0 {
		t.Fatal("root did not account all 31 actual physical attempts", e, usage)
	}
	jobs, e := s.Store.ListJobs(ctx, c.ID, 0, 100)
	if e != nil || len(jobs) != 1 || jobs[0].ID != parent.ID {
		t.Fatal("batch children leaked extra public jobs", e)
	}
	// Merely observing again cannot repeat any accepted segment or refill limits.
	totalAttempts := func() int {
		n := 0
		for _, calls := range counts() {
			n += calls
		}
		return n
	}
	before := totalAttempts()
	if _, e = s.Store.ReadJobWorkspace(ctx, parent.ID); e != nil || before != totalAttempts() {
		t.Fatal("observation made requests", e)
	}
}

func TestUnlistedBatchIDsRequireOneRegionWithoutInventingPairs(t *testing.T) {
	s, cloud, c, counts := batchSDKFixture(t)
	cloud.Grant.Regions = []string{"cn-hangzhou", "cn-beijing"}
	turn := goalTurn(t, s, c.ID, "ambiguous-pairs", "分批检查ECS i-batch-public-1 i-batch-public-2 i-batch-public-3 入向")
	if turn.Proposal != nil && turn.Proposal.PlanID != "" || turn.Intent.Status != "needs_clarification" || len(counts()) != 0 || len(turn.Intent.Selection) != 0 || len(turn.Intent.Query.ResourceIDs) != 3 {
		t.Fatal("unknown ID/region pairs were fabricated", turn.Intent)
	}
}

func TestSignedBatchCannotRefillAcceptedTotalPhysicalAllowancePerSegment(t *testing.T) {
	s, cloud, c, counts := batchSDKFixture(t, 20)
	ctx := context.Background()
	offer := goalTurn(t, s, c.ID, "bounded-unlisted-offer", "分批检查杭州 ECS i-batch-public-1 i-batch-public-2 i-batch-public-3 i-batch-public-4 i-batch-public-5 入向，只运行规则")
	if offer.Proposal == nil || offer.Proposal.PlanID == "" {
		t.Fatal("bounded full-set preview missing", offer.Intent.Status)
	}
	if _, e := s.Store.AcceptIntentProposal(ctx, c.ID, offer.Proposal.ID, "bounded-unlisted-accept", s.Plans.ConfigRevision, offer.Intent.Revision); e != nil {
		t.Fatal(e)
	}
	parent, found, e := s.Store.Claim(ctx)
	if e != nil || !found {
		t.Fatal(e)
	}
	w := NewWorkflow(s.Store)
	w.Cloud = cloud
	if e = w.Run(ctx, parent); e == nil {
		t.Fatal("twenty accepted physical attempts became thirty-one")
	}
	view, e := s.Store.ReadJobWorkspace(ctx, parent.ID)
	if e != nil || view.Batch == nil || view.Job.Status != "partial" || len(view.Batch.Covered) < 2 || len(view.Batch.Unprocessed) == 0 || len(view.Batch.Covered)+len(view.Batch.Failed)+len(view.Batch.Unprocessed) != 5 {
		t.Fatal("total exhaustion lost actual partial/unprocessed coverage", e, view.Batch)
	}
	actual := 0
	for _, n := range counts() {
		actual += n
	}
	usage, e := s.Store.BudgetUsage(ctx, s.Plans.BudgetRef, "", parent.ID)
	if e != nil || actual > 20 || usage.CloudAttempts != int64(actual) || view.Batch.Usage.CloudAttempts != int64(actual) {
		t.Fatal("segment reset bypassed root physical ledger", e, actual, usage)
	}
}

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cfw-policy-llm-agent/server/internal/outbound"
	cloudfw "github.com/alibabacloud-go/cloudfw-20171207/v11/client"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
	credential "github.com/aliyun/credentials-go/credentials"
)

type e3SDKHTTP struct{ client *http.Client }

func (h e3SDKHTTP) Call(req *http.Request, _ *http.Transport) (*http.Response, error) {
	return h.client.Do(req)
}

type e3Credential struct {
	credential.Credential
	mu       sync.Mutex
	ctx      context.Context
	client   *http.Client
	endpoint string
	cached   *credential.CredentialModel
}

func (c *e3Credential) GetCredential() (*credential.CredentialModel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != nil {
		return c.cached, nil
	}
	req, err := http.NewRequestWithContext(c.ctx, "POST", c.endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("synthetic credential exchange failed")
	}
	var model credential.CredentialModel
	if json.NewDecoder(response.Body).Decode(&model) != nil {
		return nil, errors.New("synthetic credential decode failed")
	}
	c.cached = &model
	return c.cached, nil
}

func TestE3PersistentLedgerCountsSDKRetriesAndCredentialHTTP(t *testing.T) {
	ctx := context.Background()
	store, initial, job := e3BudgetCase(t, 3, 0, 3, 0)
	var credentials, cloud atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/credential" {
			credentials.Add(1)
			io.WriteString(w, `{"accessKeyId":"synthetic-access-key","accessKeySecret":"synthetic-access-secret","type":"access_key"}`)
			return
		}
		if cloud.Add(1) == 1 {
			w.WriteHeader(500)
			io.WriteString(w, `{"Code":"InternalError","Message":"PRIVATE_UPSTREAM_BODY"}`)
			return
		}
		io.WriteString(w, `{"RequestId":"synthetic-request","TotalCount":0,"NatFirewallList":[]}`)
	}))
	defer upstream.Close()
	manager, err := outbound.NewManager(store, outbound.Config{Revision: initial.ConfigRevision, Sources: []outbound.Source{
		{Ref: "cloud-cfw", Class: "cloud", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []outbound.Route{{Method: "POST", Path: "/", Actions: []string{"DescribeNatFirewallList"}}}},
		{Ref: "credential-role", Class: "credential", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []outbound.Route{{Method: "POST", Path: "/credential"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	cloudHTTP, err := manager.Client(ctx, job.ID, "cloud-cfw")
	if err != nil {
		t.Fatal(err)
	}
	credentialHTTP, err := manager.Client(ctx, job.ID, "credential-role")
	if err != nil {
		t.Fatal(err)
	}
	static, err := credential.NewCredential(&credential.Config{Type: dara.String("access_key"), AccessKeyId: dara.String("synthetic-access-key"), AccessKeySecret: dara.String("synthetic-access-secret")})
	if err != nil {
		t.Fatal("synthetic credential setup failed")
	}
	provider := &e3Credential{Credential: static, ctx: ctx, client: credentialHTTP, endpoint: upstream.URL + "/credential"}
	u, _ := url.Parse(upstream.URL)
	client, err := cloudfw.NewClient(&openapi.Config{Endpoint: dara.String(u.Host), Protocol: dara.String("http"), RegionId: dara.String("cn-hangzhou"), Credential: provider, HttpClient: e3SDKHTTP{client: cloudHTTP}, RetryOptions: &dara.RetryOptions{Retryable: true, MaxAttempts: 2}})
	if err != nil {
		t.Fatal("synthetic SDK initialization failed")
	}
	if credentials.Load() != 0 || cloud.Load() != 0 {
		t.Fatal("SDK/provider construction emitted HTTP")
	}
	read := func() error {
		_, err := client.DescribeNatFirewallListWithContext(ctx, &cloudfw.DescribeNatFirewallListRequest{PageNo: dara.Int64(1), PageSize: dara.Int64(10), NatGatewayId: dara.String("synthetic-nat")}, &dara.RuntimeOptions{})
		return err
	}
	if err = read(); err != nil {
		t.Fatal("controlled SDK read failed", outbound.ErrorCode(err))
	}
	usage, err := store.BudgetUsage(ctx, initial.BudgetRef, "", "")
	if err != nil || credentials.Load() != 1 || cloud.Load() != 2 || usage.CloudAttempts != 3 || usage.UnknownDelivery != 0 {
		t.Fatalf("physical/durable mismatch: credential=%d cloud=%d usage=%+v err=%v", credentials.Load(), cloud.Load(), usage, err)
	}
	if err = read(); err == nil {
		t.Fatal("SDK logical retry received a fresh allowance")
	}
	if credentials.Load() != 1 || cloud.Load() != 2 {
		t.Fatal("exhausted budget still sent HTTP")
	}
	attempts, err := store.JobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(attempts)
	for _, private := range []string{"synthetic-access-secret", "synthetic-access-key", "PRIVATE_UPSTREAM_BODY", upstream.URL} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("attempt ledger retained private endpoint, credential or provider text")
		}
	}
}

func TestE3BudgetRestartRetainsReservedAttemptsAndPeriod(t *testing.T) {
	ctx := context.Background()
	store, initial, job := e3BudgetCase(t, 0, 2, 0, 2)
	for range 2 {
		if _, err := store.ReserveAttempt(ctx, job.ID, "model", "configured-model", initial.ConfigRevision); err != nil {
			t.Fatal(err)
		}
	}
	var path string
	if err := store.SQL.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	policy, err := reopened.GetBudgetPolicy(ctx, initial.BudgetRef)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.ConfigureBudgetPolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	conversation, err := reopened.CreateConversation(ctx, "重启后的新会话", initial.PolicyScopeRef)
	if err != nil {
		t.Fatal(err)
	}
	initial.ConversationID, initial.ConversationRevision = conversation.ID, conversation.Revision
	plan, err := reopened.SavePlan(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := reopened.EnqueuePlan(ctx, plan.ID, "e3-after-restart")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reopened.Claim(ctx); err != nil || !found {
		t.Fatal(err)
	}
	if _, err = reopened.ReserveAttempt(ctx, next.ID, "model", "configured-model", initial.ConfigRevision); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("restart/new conversation reset old period", err)
	}
	usage, err := reopened.BudgetUsage(ctx, initial.BudgetRef, "", "")
	if err != nil || usage.ModelAttempts != 2 || usage.UnknownDelivery != 2 {
		t.Fatal("unknown sends were refunded on restart", err)
	}
}

func TestE3PersistentLedgerWriteFailureStopsPhysicalHTTP(t *testing.T) {
	for _, stage := range []string{"reserve", "record"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			store, initial, job := e3BudgetCase(t, 0, 5, 0, 5)
			trigger := "CREATE TRIGGER reject_e3_write BEFORE INSERT ON outbound_attempts BEGIN SELECT RAISE(ABORT,'PRIVATE_STORAGE_FAILURE'); END"
			if stage == "record" {
				trigger = "CREATE TRIGGER reject_e3_write BEFORE UPDATE ON outbound_attempts BEGIN SELECT RAISE(ABORT,'PRIVATE_STORAGE_FAILURE'); END"
			}
			if err := store.db.Exec(trigger).Error; err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) }))
			defer upstream.Close()
			manager, err := outbound.NewManager(store, outbound.Config{Revision: initial.ConfigRevision, Sources: []outbound.Source{{Ref: "configured-model", Class: "model", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []outbound.Route{{Method: "POST", Path: "/v1/chat/completions"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			client, err := manager.Client(ctx, job.ID, "configured-model")
			if err != nil {
				t.Fatal(err)
			}
			wantCode, wantCalls := "ADMISSION_UNAVAILABLE", int32(0)
			if stage == "record" {
				wantCode, wantCalls = "ATTEMPT_RECORD_FAILED", 1
			}
			for range 2 {
				request, _ := http.NewRequestWithContext(ctx, "POST", upstream.URL+"/v1/chat/completions", strings.NewReader(`{}`))
				if _, err = client.Do(request); outbound.ErrorCode(err) != wantCode {
					t.Fatal("write failure escaped safe guard", outbound.ErrorCode(err))
				}
			}
			if calls.Load() != wantCalls {
				t.Fatal("write failure allowed later physical requests")
			}
			usage, err := store.BudgetUsage(ctx, initial.BudgetRef, "", "")
			if err != nil || usage.ModelAttempts != int64(wantCalls) || usage.UnknownDelivery != int64(wantCalls) {
				t.Fatal("write failure lost unknown reservations", err)
			}
		})
	}
}

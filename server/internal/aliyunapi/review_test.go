package aliyunapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFailedIdentityRecheckClosesPreviouslyVerifiedReader(t *testing.T) {
	var count atomic.Int32
	r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			identity(w)
			return
		}
		w.WriteHeader(500)
		io.WriteString(w, `{"Code":"Denied","Message":"private"}`)
	}))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := r.VerifyIdentity(context.Background()); e == nil {
		t.Fatal("failed recheck accepted")
	}
	if _, e := r.Read(context.Background(), "DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50"}); ErrorCode(e) != "CLOUD_IDENTITY_NOT_VERIFIED" || count.Load() != 2 {
		t.Fatal("failed recheck retained prior identity authorization", e, count.Load())
	}
}
func TestRawCredentialReflectionAndDuplicateConditionKeysAreRejected(t *testing.T) {
	for _, body := range []string{`{"Policys":[],"TotalCount":"0","Description":"synthetic-access-secret"}`, `{"Policys":[],"TotalCount":"0","Nested":{"source":"a","source":"b"}}`, `{"Code":"Denied","Message":"PRIVATE_PROVIDER_BODY"}`} {
		t.Run(body, func(t *testing.T) {
			r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if action(r) == "GetCallerIdentity" {
					identity(w)
					return
				}
				io.WriteString(w, body)
			}))
			if e := r.VerifyIdentity(context.Background()); e != nil {
				t.Fatal(e)
			}
			b, e := r.Read(context.Background(), "DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50"})
			if e == nil || b != nil || strings.Contains(e.Error(), "PRIVATE") || strings.Contains(e.Error(), "secret") {
				t.Fatal("unsafe raw response escaped", e)
			}
		})
	}
}
func TestECSCannotExpandSecurityGroupScopeBeforeAssociation(t *testing.T) {
	var calls atomic.Int32
	r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); identity(w) }))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Read(context.Background(), "DescribeSecurityGroupAttribute", map[string]string{"RegionId": "cn-hangzhou", "SecurityGroupId": "sg-unrelated", "MaxResults": "50"}); e == nil || calls.Load() != 1 {
		t.Fatal("unassociated group reached network")
	}
}

func TestOversizedResponseAndOwnerScopeMismatchStayControlled(t *testing.T) {
	for _, body := range []string{`{"Policys":[],"TotalCount":"0","AliUid":9999999999999999}`, `{"Policys":[],"TotalCount":"0","pad":"` + strings.Repeat("x", MaxResponseBytes) + `"}`} {
		t.Run("bounded-response", func(t *testing.T) {
			r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if action(r) == "GetCallerIdentity" {
					identity(w)
					return
				}
				io.WriteString(w, body)
			}))
			if e := r.VerifyIdentity(context.Background()); e != nil {
				t.Fatal(e)
			}
			b, e := r.Read(context.Background(), "DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50"})
			if e == nil || b != nil || strings.Contains(e.Error(), "9999999") {
				t.Fatal("unbounded or foreign-owner body escaped", e)
			}
		})
	}
}

func TestCancelledIdentityRecheckAlsoClosesGate(t *testing.T) {
	r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { identity(w) }))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := r.VerifyIdentity(ctx); e == nil {
		t.Fatal("cancelled identity verification succeeded")
	}
	if _, e := r.Read(context.Background(), "DescribeControlPolicy", map[string]string{"Direction": "in", "CurrentPage": "1", "PageSize": "50"}); ErrorCode(e) != "CLOUD_IDENTITY_NOT_VERIFIED" {
		t.Fatal("cancelled recheck retained identity gate", e)
	}
}

func TestCFWAssetLookupRequiresConfiguredResourceRatherThanRegionSweep(t *testing.T) {
	r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { identity(w) }))
	if e := r.VerifyIdentity(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, search := range []string{"", "i-unrelated"} {
		q := map[string]string{"RegionNo": "cn-hangzhou", "CurrentPage": "1", "PageSize": "50", "IpVersion": "4"}
		if search != "" {
			q["SearchItem"] = search
		}
		if _, e := r.Read(context.Background(), "DescribeAssetList", q); e == nil {
			t.Fatal("asset read expanded to region or unrelated instance")
		}
	}
}

func TestAssetLookupSeparatesControlPlaneFromReturnedAssetRegion(t *testing.T) {
	for _, row := range []string{`{"ResourceInstanceId":"eip-synthetic","BindInstanceId":"i-synthetic","RegionID":"cn-hangzhou"}`, `{"ResourceInstanceId":"i-synthetic","RegionID":"cn-beijing"}`, `{"ResourceInstanceId":"i-unrelated","RegionID":"cn-hangzhou"}`} {
		t.Run(row, func(t *testing.T) {
			r, _ := readerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if action(r) == "GetCallerIdentity" {
					identity(w)
					return
				}
				if r.Form.Get("RegionNo") != "" {
					t.Error("asset region confused with control-plane filter")
				}
				io.WriteString(w, `{"Assets":[`+row+`],"TotalCount":1}`)
			}))
			if e := r.VerifyIdentity(context.Background()); e != nil {
				t.Fatal(e)
			}
			b, e := r.Read(context.Background(), "DescribeAssetList", map[string]string{"SearchItem": "i-synthetic", "CurrentPage": "1", "PageSize": "50", "IpVersion": "4"})
			if strings.Contains(row, "eip-synthetic") {
				if e != nil || b == nil {
					t.Fatal("explicit instance+returned region lookup failed", e)
				}
			} else if ErrorCode(e) != "CLOUD_ASSET_SCOPE_MISMATCH" || b != nil {
				t.Fatal("cross-region or unrelated asset escaped", e)
			}
		})
	}
}

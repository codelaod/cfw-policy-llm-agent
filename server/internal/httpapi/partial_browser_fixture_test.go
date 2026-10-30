//go:build e4browserfixture

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

// Explicit browser-only fixture, never compiled into the product binary.
// The actual SDK reads public loopback responses. The malformed final ACL
// exercises safe observation persistence, rather than a synthetic success row.
func TestPartialCollectionBrowserServe(t *testing.T) {
	root, e := filepath.Abs(filepath.Join("..", "..", ".."))
	if e != nil {
		t.Fatal(e)
	}
	statePath := filepath.Join(root, "runtime", "partial-browser-service.json")
	stopPath := filepath.Join(root, "runtime", "partial-browser-stop")
	if _, e = os.Stat(statePath); !os.IsNotExist(e) {
		t.Fatal("resolve the previous explicit browser fixture before starting another")
	}
	f := startE4Fixture(t, t.TempDir(), "malformed_acl")
	cloud := f.plans.Cloud
	var base aliyuncollector.Config
	for _, cfg := range cloud.Scopes {
		if cfg.Scope.Boundary == "internet" && cfg.Scope.Direction == "in" {
			base = cfg
		}
	}
	cloud.Grant = storage.ConnectionGrant{SourceRef: cloud.SourceRef, ConfigRevision: cloud.ConfigRevision, AccountRef: base.Scope.AccountRef, ControlPlaneRef: base.Scope.ControlPlaneRef, Regions: []string{"cn-hangzhou"}, Boundaries: []string{"internet/in"}}
	cloud.DiscoveryConfig = base
	cloud.DiscoveryConfig.SourceRef = cloud.SourceRef
	cloud.DiscoveryConfig.AllowedRegions = []string{"cn-hangzhou"}
	cloud.ResolveScope = func(boundary, direction string, resources []domain.Resource) (aliyuncollector.Config, error) {
		if boundary != "internet" || direction != "in" {
			return base, app.ErrScopeDenied
		}
		for _, r := range resources {
			if r.ResourceID != "i-synthetic-browser" || r.Region != "cn-hangzhou" || r.ResourceType != "ecs" {
				return base, app.ErrScopeDenied
			}
		}
		return base, nil
	}
	ctx := context.Background()
	if e = f.store.RegisterConnectionGrant(ctx, cloud.Grant); e != nil {
		t.Fatal(e)
	}
	resource := domain.Resource{Provider: "aliyun", AccountRef: base.Scope.AccountRef, ResourceID: "i-synthetic-browser", Region: "cn-hangzhou", ResourceType: "ecs", Name: "failure-web", Source: "live_readonly", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if e = f.store.UpsertResources(ctx, cloud.SourceRef, []domain.Resource{resource}, domain.CatalogueCoverage{AccountRef: base.Scope.AccountRef, Source: cloud.SourceRef, Complete: false, Limitations: []string{"PUBLIC_ARTIFICIAL_FIXTURE"}}); e != nil {
		t.Fatal(e)
	}
	state, _ := json.Marshal(map[string]string{"base_url": f.api.URL, "application_token": f.token, "stop_path": stopPath, "source": "public artificial signed SDK loopback only; malformed ACL preserves prior safe observations"})
	if e = os.WriteFile(statePath, state, 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.Remove(statePath); os.Remove(stopPath) })
	fmt.Printf("Partial-collection local browser fixture ready at %s; state runtime/partial-browser-service.json\n", f.api.URL)
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		if _, e = os.Stat(stopPath); e == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("explicit local browser fixture window expired")
}

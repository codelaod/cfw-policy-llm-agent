package app

import (
	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
	"context"
	"fmt"
	"testing"
)

func TestIndependentReviewTagSelectorDoesNotBecomeResourceName(t *testing.T) {
	s, c := conversationalCase(t)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	in.SnapshotVersion = "review-tag-selector"
	in.Assets[0].Name = "web-prod"
	in.Assets[0].ProviderResourceID = "i-tag-web"
	in.Assets[0].Region = "cn-hangzhou"
	in.Assets[0].ResourceType = "ecs"
	in.Assets[0].Tags = map[string]string{"app": "web-prod"}
	second := in.Assets[0]
	second.ID = "asset-tag-worker"
	second.Name = "worker-prod"
	second.ProviderResourceID = "i-tag-worker"
	in.Assets = append(in.Assets, second)
	in.Selection.AssetIDs = append(in.Selection.AssetIDs, second.ID)
	if _, err = s.Store.SaveSnapshot(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	// The initial independent reproduction exposed the name/tag collision, but
	// SaveSnapshot alone records snapshot_only_not_account_inventory (catalog.go).
	// Supply the finite synthetic directory observation required for selecting all;
	// keep the original Query.Name/Selection=2 assertions unchanged.
	page, err := s.Store.SearchResources(context.Background(), storage.ResourceFilter{SourceRef: "local-offline", AccountRef: in.Scope.AccountRef, Region: "cn-hangzhou", ResourceType: "ecs", Tags: map[string]string{"app": "web-prod"}}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	coverage := domain.CatalogueCoverage{AccountRef: in.Scope.AccountRef, Source: "local-offline", Selector: domain.ResourceQuery{Region: "cn-hangzhou", ResourceType: "ecs", Tags: map[string]string{"app": "web-prod"}}, Complete: true, ObservedAt: in.CollectedAt}
	if err = s.Store.UpsertResources(context.Background(), "local-offline", page.Items, coverage); err != nil {
		t.Fatal(err)
	}
	r := goalTurn(t, s, c.ID, "review-tag-selector", "审计杭州标签 app=web-prod 的所有ECS入向，只运行规则")
	if r.Intent.Query.Name != "" || len(r.Intent.Selection) != 2 {
		t.Fatalf("tag value narrowed requested all-assets scope: query=%+v selection=%+v candidates=%+v status=%s", r.Intent.Query, r.Intent.Selection, r.Intent.Candidates, r.Intent.Status)
	}
}

func TestIndependentReviewChangingIDDoesNotInheritPreviousTagFilter(t *testing.T) {
	s, c := conversationalCase(t)
	first := goalTurn(t, s, c.ID, "review-old-tags", "审计杭州所有ECS，标签 env=prod，只运行规则")
	if first.Intent.Query.Tags["env"] != "prod" {
		t.Fatal(first.Intent.Query)
	}
	r := goalTurn(t, s, c.ID, "review-new-id", "改查 i-web-cn-beijing 的ECS入向，只运行规则")
	if len(r.Intent.Query.Tags) != 0 || len(r.Intent.Selection) != 1 || r.Intent.Selection[0].ResourceID != "i-web-cn-beijing" {
		t.Fatalf("new explicit ID inherited unrelated tag filter: query=%+v selection=%+v status=%s", r.Intent.Query, r.Intent.Selection, r.Intent.Status)
	}
}

func TestIndependentReviewIncompleteTagDirectoryDoesNotSelectAll(t *testing.T) {
	s, c := conversationalCase(t)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	in.SnapshotVersion = "review-incomplete-tag-selector"
	in.Assets[0].Name = "web-prod"
	in.Assets[0].ProviderResourceID = "i-incomplete-tag-web"
	in.Assets[0].Region = "cn-hangzhou"
	in.Assets[0].ResourceType = "ecs"
	in.Assets[0].Tags = map[string]string{"app": "web-prod"}
	second := in.Assets[0]
	second.ID = "asset-incomplete-tag-worker"
	second.Name = "worker-prod"
	second.ProviderResourceID = "i-incomplete-tag-worker"
	in.Assets = append(in.Assets, second)
	in.Selection.AssetIDs = append(in.Selection.AssetIDs, second.ID)
	if _, err = s.Store.SaveSnapshot(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	r := goalTurn(t, s, c.ID, "review-incomplete-tag-selector", "审计杭州标签 app=web-prod 的所有ECS入向，只运行规则")
	if r.Intent.Query.Name != "" || r.Intent.Catalogue.Complete || len(r.Intent.Selection) != 0 || len(r.Intent.Candidates) != 2 || r.Proposal != nil {
		t.Fatalf("incomplete directory promoted to all: %+v", r.Intent)
	}
}

func TestIndependentReviewCompleteAllSelectorTraversesPagesBeforeBatchChoice(t *testing.T) {
	s, c := conversationalCase(t)
	rows := []domain.Resource{}
	for i := 0; i < 101; i++ {
		rows = append(rows, domain.Resource{Provider: "aliyun", AccountRef: "review-page-account", ResourceID: fmt.Sprintf("i-page-%03d", i), ResourceType: "ecs", Region: "cn-hangzhou", Name: fmt.Sprintf("page-node-%03d", i), Tags: map[string]string{"app": "web-prod"}, Source: "imported", ObservedAt: "2026-10-29T01:00:00Z"})
	}
	coverage := domain.CatalogueCoverage{AccountRef: "review-page-account", Source: "review-pages", Selector: domain.ResourceQuery{Region: "cn-hangzhou", ResourceType: "ecs", Tags: map[string]string{"app": "web-prod"}}, Complete: true, ObservedAt: "2026-10-29T01:00:00Z"}
	if err := s.Store.UpsertResources(context.Background(), "review-pages", rows, coverage); err != nil {
		t.Fatal(err)
	}
	r := goalTurn(t, s, c.ID, "review-all-pages", "审计杭州所有ECS，标签 app=web-prod，只运行规则")
	if len(r.Intent.Selection) != 101 || len(r.Intent.Candidates) != 0 || !r.Intent.Catalogue.Complete || r.Intent.Status != "needs_batch" {
		t.Fatalf("complete all selector stopped at first page: %+v", r.Intent)
	}
}

func TestIndependentReviewSingleObservedObjectDoesNotProveAll(t *testing.T) {
	s, c := conversationalCase(t)
	r := goalTurn(t, s, c.ID, "review-incomplete-single", "审计北京所有ECS入向，只运行规则")
	if r.Intent.Catalogue.Complete || len(r.Intent.Selection) != 0 || len(r.Intent.Candidates) != 1 || r.Proposal != nil {
		t.Fatalf("single seen object promoted to whole region: %+v", r.Intent)
	}
}

func TestIndependentReviewFiniteRegionOnlyAuditCanPrepareInOriginalJob(t *testing.T) {
	s, cloud, c, physical := preparationIntegration(t)
	job, plan := executeGoal(t, s, cloud, c, "review-region-all", "审计杭州的ECS互联网入向，只运行规则")
	if plan.Preparation == nil || plan.Preparation.Query.Name != "" || len(queryIDs(plan.Preparation.Query)) != 0 || !plan.Preparation.AllowMultiple || physical.Load() == 0 {
		t.Fatal("region selector not retained as bounded preparation", plan)
	}
	view, err := s.Store.ReadJobWorkspace(context.Background(), job.ID)
	if err != nil || view.Audit == nil || !view.ReportPublished || view.ReportRef == "" || len(view.Audit.Input.Assets) != 1 {
		t.Fatal("finite region audit did not produce durable result", err)
	}
}

func TestIndependentReviewAllWithinConfirmedGroupDoesNotWidenToRegion(t *testing.T) {
	s, c, _ := selectedGroupDialog(t, 2)
	group := confirmDialogGroup(t, s, c)
	scoped := goalTurn(t, s, c.ID, "review-confirmed-group-scope", "审计客户甲的全部ECS入向，只运行规则")
	if scoped.Intent.ResourceGroupVersionID != group.ID || len(scoped.Intent.Selection) != 2 {
		t.Fatal(scoped.Intent)
	}
	all := goalTurn(t, s, c.ID, "review-confirmed-group-all", "审计这组全部对象，只运行规则")
	if all.Intent.ResourceGroupVersionID != group.ID || all.Intent.Query.Group != "客户甲" || len(all.Intent.Selection) != 2 {
		t.Fatalf("group-relative all widened its target: %+v", all.Intent)
	}
}

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func TestCatalogIdentityDisambiguationAndHistoricalBinding(t *testing.T) {
	ctx := context.Background()
	s := collaborationStore(t)
	in, _ := audit.SyntheticExample("internet-in")
	in.Assets[0].Name = "web-prod"
	in.Assets[0].Region = "cn-hangzhou"
	in.Assets[0].ResourceType = "ecs"
	in.Assets[0].ProviderResourceID = "i-web-hz"
	first, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.SnapshotVersion = "second"
	in.Assets[0].ID = "asset-bj"
	in.Assets[0].ProviderResourceID = "i-web-bj"
	in.Assets[0].Region = "cn-beijing"
	in.Selection.AssetIDs = []string{"asset-bj"}
	second, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchResources(ctx, ResourceFilter{Name: "web-prod", ResourceType: "ecs"}, "", 100)
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	page, err = s.SearchResources(ctx, ResourceFilter{ResourceID: "i-web-hz"}, "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].Region != "cn-hangzhou" {
		t.Fatal(page, err)
	}
	bindings, err := s.ResourceBindings(ctx, []string{page.Items[0].ID}, "internet", "in")
	if err != nil || len(bindings) != 1 || bindings[0].SnapshotID != first.ID {
		t.Fatal(bindings, second.ID, err)
	}
	in.Assets[0].Name = "renamed"
	_, err = s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.GetSnapshot(ctx, second.ID)
	if err != nil || old.Input.Assets[0].Name != "web-prod" {
		t.Fatal("catalogue update changed snapshot", err)
	}
}
func TestResourceCatalogueStablePagingAndCoverage(t *testing.T) {
	ctx := context.Background()
	s := collaborationStore(t)
	for n := 0; n < 3; n++ {
		r := domain.Resource{Provider: "aliyun", AccountRef: "account", Region: "cn-hangzhou", ResourceType: "ecs", ResourceID: []string{"i-c", "i-a", "i-b"}[n], Name: "shared", Source: "imported", ObservedAt: "2026-10-29T01:00:00Z"}
		if err := s.UpsertResources(ctx, "source", []domain.Resource{r}, domain.CatalogueCoverage{Complete: false, Limitations: []string{"page_missing"}}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.SearchResources(ctx, ResourceFilter{Name: "shared"}, "", 2)
	if err != nil || len(p.Items) != 2 || !p.HasMore || p.Coverage.Complete {
		t.Fatal(p, err)
	}
	q, err := s.SearchResources(ctx, ResourceFilter{Name: "shared"}, p.NextCursor, 2)
	if err != nil || len(q.Items) != 1 || q.HasMore || q.Items[0].ID == p.Items[0].ID {
		t.Fatal(q, err)
	}
	if _, err = s.SearchResources(ctx, ResourceFilter{}, "not-a-cursor", 2); !errors.Is(err, ErrCursor) {
		t.Fatal(err)
	}
}
func TestWorkflowSchemaMigrationIsExplicitAndChecksAllStructures(t *testing.T) {
	p := filepath.Join(t.TempDir(), "workflow.sqlite")
	s, err := Open(p, true)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := audit.SyntheticExample("internet-in")
	snapshot, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("DROP TABLE conversation_intent_heads").Error; err != nil {
		t.Fatal(err)
	}
	s.Close()
	if unexpected, e := Open(p, false); e == nil {
		unexpected.Close()
		t.Fatal("normal startup accepted incomplete workflow schema")
	}
	s, err = Open(p, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.GetSnapshot(context.Background(), snapshot.ID); err != nil {
		t.Fatal(err)
	}
	var v int
	s.db.Raw("SELECT version FROM business_schema").Scan(&v)
	if v != 5 {
		t.Fatal(v)
	}
}

func TestCatalogueProvenanceAndCoverageNeverCrossScope(t *testing.T) {
	ctx := context.Background()
	s := collaborationStore(t)
	r := domain.Resource{Provider: "aliyun", AccountRef: "account-a", Region: "cn-hangzhou", ResourceType: "ecs", ResourceID: "i-a", Name: "web", Source: "live_readonly", ObservedAt: "2026-10-29T01:00:00Z"}
	coverage := domain.CatalogueCoverage{Complete: true, AccountRef: "account-a", Selector: domain.ResourceQuery{Region: "cn-hangzhou", ResourceType: "ecs"}}
	if err := s.UpsertResources(ctx, "cloud", []domain.Resource{r}, coverage); err != nil {
		t.Fatal(err)
	}
	r.Source = "imported"
	r.Name = "import-name"
	if err := s.UpsertResources(ctx, "local-offline", []domain.Resource{r}, domain.CatalogueCoverage{}); err != nil {
		t.Fatal(err)
	}
	page, err := s.SearchResources(ctx, ResourceFilter{SourceRef: "cloud", AccountRef: "account-a", Region: "cn-hangzhou", ResourceType: "ecs"}, "", 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "web" || !page.Coverage.Complete {
		t.Fatal(page, err)
	}
	for _, f := range []ResourceFilter{{SourceRef: "cloud", AccountRef: "account-b", Region: "cn-hangzhou", ResourceType: "ecs"}, {SourceRef: "cloud", AccountRef: "account-a", Region: "cn-beijing", ResourceType: "ecs"}, {SourceRef: "cloud", AccountRef: "account-a", Region: "cn-hangzhou", ResourceType: "nat_gateway"}, {SourceRef: "cloud"}} {
		p, e := s.SearchResources(ctx, f, "", 100)
		if e != nil || p.Coverage.Complete {
			t.Fatal("coverage escaped its enumeration selector", p, e)
		}
	}
}

func TestIntentRevisionAtomicMessagesReplayAndReopen(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "intent.sqlite")
	s, err := Open(p, true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateConversation(ctx, "first goal", "")
	if err != nil {
		t.Fatal(err)
	}
	req := SaveIntentTurn{ConversationID: c.ID, ExpectedConversationRevision: c.Revision, RequestKey: "turn-one", RequestDigest: "digest-one", Text: "检查杭州 web-prod", Reply: "找到同名对象，请选择地域。", Intent: domain.ConversationIntent{Status: "needs_clarification", Action: "audit", Mode: "rules_only"}}
	a, err := s.SaveIntentRevision(ctx, req)
	if err != nil || a.Intent.Revision != 1 || a.Message.Seq != 1 || a.Reply.Seq != 2 {
		t.Fatal(a, err)
	}
	b, err := s.SaveIntentRevision(ctx, req)
	if err != nil || !b.Replayed || b.Message.ID != a.Message.ID {
		t.Fatal(b, err)
	}
	req.RequestDigest = "different"
	if _, err = s.SaveIntentRevision(ctx, req); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	req.RequestKey = "turn-two"
	req.RequestDigest = "digest-two"
	if _, err = s.SaveIntentRevision(ctx, req); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("old goal revision accepted", err)
	}
	s.Close()
	s, err = Open(p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err := s.CurrentIntent(ctx, c.ID)
	if err != nil || current.Revision != 1 || current.Status != "needs_clarification" {
		t.Fatal(current, err)
	}
	messages, err := s.ConversationMessages(ctx, c.ID, 0, 100)
	if err != nil || len(messages.Items) != 2 {
		t.Fatal(messages, err)
	}
}

func TestCatalogueMissingRevisionRowCannotIssueStableCursor(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "missing-revision.sqlite")
	s, e := Open(p, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.db.Exec("DELETE FROM resource_catalogue_state").Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.SearchResources(ctx, ResourceFilter{}, "", 10); e == nil {
		t.Fatal("missing revision treated as zero")
	}
	r := domain.Resource{Provider: "aliyun", AccountRef: "account", ResourceID: "i-a", Name: "a", Source: "synthetic", ObservedAt: "2026-10-29T00:00:00Z"}
	if e = s.UpsertResources(ctx, "local", []domain.Resource{r}, domain.CatalogueCoverage{}); e == nil {
		t.Fatal("upsert committed without revision")
	}
	s.Close()
	if unexpected, e := Open(p, false); e == nil {
		unexpected.Close()
		t.Fatal("startup accepted missing mandatory singleton")
	}
	s, e = Open(p, true)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
}

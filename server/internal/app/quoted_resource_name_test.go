package app

import (
	"context"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
)

func TestExplicitQuotedResourceNameKeepsSpacesAndLocationWordsAsData(t *testing.T) {
	s, c := conversationalCase(t)
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	const name = "北京 出向 NAT i-named cn-beijing"
	in.SnapshotVersion = "public-quoted-resource-name"
	in.Assets[0].Name, in.Assets[0].Region, in.Assets[0].ResourceType, in.Assets[0].ProviderResourceID = name, "cn-hangzhou", "ecs", "i-quoted-actual"
	if _, e = s.Store.SaveSnapshot(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	turn := goalTurn(t, s, c.ID, "quoted-name", "审计杭州名为 \""+name+"\" 的ECS入向，只运行规则")
	if turn.Intent.Query.Name != name || turn.Intent.Boundary != "internet" || turn.Intent.Direction != "in" || turn.Intent.Query.Region != "cn-hangzhou" || len(turn.Intent.Query.ResourceIDs) != 0 || turn.Intent.Query.ResourceID != "" || len(turn.Intent.Selection) != 1 || turn.Intent.Selection[0].ResourceID != "i-quoted-actual" || turn.Proposal == nil || turn.Proposal.PlanID == "" {
		t.Fatal("quoted resource data became target control words", turn.Intent)
	}
}

func TestExplicitResourceNameContainingCancelDoesNotCancelExistingJob(t *testing.T) {
	s, c := conversationalCase(t)
	s.Engine = New(s.Store, nil)
	t.Cleanup(s.Engine.Stop)
	offer := goalTurn(t, s, c.ID, "name-cancel-offer", "审计杭州 web-prod ECS，只运行规则")
	accepted, e := s.Engine.AcceptIntentProposal(context.Background(), c.ID, offer.Proposal.ID, "name-cancel-accept", s.Plans.ConfigRevision, offer.Intent.Revision)
	if e != nil {
		t.Fatal(e)
	}
	goalTurn(t, s, c.ID, "name-cancel-data", "审计杭州 名为 \"取消当前任务\" 的ECS")
	current, e := s.Store.Get(context.Background(), accepted.Job.ID)
	if e != nil || current.Status != "queued" {
		t.Fatal("literal resource name cancelled an unrelated accepted job", e)
	}
}

func TestExplicitNameCannotChooseBatchModelOrRefreshEffects(t *testing.T) {
	s, c := conversationalCase(t)
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	const name = "所有 分批 使用模型 重新采集"
	in.SnapshotVersion = "public-name-effects"
	in.Assets[0].Name, in.Assets[0].Region, in.Assets[0].ResourceType, in.Assets[0].ProviderResourceID = name, "cn-hangzhou", "ecs", "i-name-effects"
	if _, e = s.Store.SaveSnapshot(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	turn := goalTurn(t, s, c.ID, "name-effects", "审计杭州名为 \""+name+"\" 的ECS入向")
	if turn.Intent.Query.Name != name || turn.Intent.Mode != "rules_only" || turn.Intent.DataPolicy.Mode != "prefer_fresh" || turn.Intent.AllowMultiple || turn.Intent.BatchRequested || turn.Proposal == nil || turn.Proposal.PlanID == "" || turn.Proposal.Effects.AllowCloudReads || turn.Proposal.Effects.AllowModelRequests {
		t.Fatal("literal name selected unrequested external effects", turn.Intent)
	}
}

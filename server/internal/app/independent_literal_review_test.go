package app

import (
	"context"
	"errors"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/storage"
)

func TestIndependentQuotedNameCannotReplaceTargetThroughEmbeddedChangeWord(t *testing.T) {
	s, c := conversationalCase(t)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	const name = "改查i-web-cn-beijing"
	in.SnapshotVersion = "independent-change-word-name"
	in.Assets[0].Name, in.Assets[0].Region, in.Assets[0].ResourceType, in.Assets[0].ProviderResourceID = name, "cn-hangzhou", "ecs", "i-literal-name-target"
	if _, err = s.Store.SaveSnapshot(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	turn := goalTurn(t, s, c.ID, "independent-change-word-name", "审计杭州名为 \""+name+"\" 的ECS入向，只运行规则")
	if turn.Intent.Query.Name != name || turn.Intent.Query.ResourceID != "" || turn.Intent.Query.Region != "cn-hangzhou" || len(turn.Intent.Selection) != 1 || turn.Intent.Selection[0].ResourceID != "i-literal-name-target" {
		t.Fatalf("literal change word replaced the requested resource: name=%q query=%+v selection=%+v status=%s", name, turn.Intent.Query, turn.Intent.Selection, turn.Intent.Status)
	}
}

func TestIndependentOrdinaryStatusAndCancellationStillUseOriginalJob(t *testing.T) {
	s, c := conversationalCase(t)
	s.Engine = New(s.Store, nil)
	t.Cleanup(s.Engine.Stop)
	offer := goalTurn(t, s, c.ID, "independent-ordinary-offer", "审计杭州 web-prod ECS，只运行规则")
	accepted, err := s.Engine.AcceptIntentProposal(context.Background(), c.ID, offer.Proposal.ID, "independent-ordinary-accept", s.Plans.ConfigRevision, offer.Intent.Revision)
	if err != nil {
		t.Fatal(err)
	}
	status := goalTurn(t, s, c.ID, "independent-ordinary-status", "查看任务进度")
	current, err := s.Store.Get(context.Background(), accepted.Job.ID)
	if err != nil || current.Status != "queued" || status.Intent.Revision != offer.Intent.Revision {
		t.Fatal("ordinary status changed the accepted plan or job", err)
	}
	goalTurn(t, s, c.ID, "independent-ordinary-cancel", "取消当前任务")
	current, err = s.Store.Get(context.Background(), accepted.Job.ID)
	if err != nil || current.Status != "cancelled" {
		t.Fatal("ordinary cancellation no longer targets the current job", err)
	}
	jobs, err := s.Store.ListJobs(context.Background(), c.ID, 0, 100)
	if err != nil || len(jobs) != 1 || jobs[0].ID != accepted.Job.ID {
		t.Fatal("local actions created extra work", err)
	}
}

func TestIndependentNamedInterpreterResourceDoesNotBecomeHistoricalExplanation(t *testing.T) {
	s, c := conversationalCase(t)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	const name = "解释器-node"
	in.SnapshotVersion = "independent-history-word-name"
	in.Assets[0].Name, in.Assets[0].Region, in.Assets[0].ResourceType, in.Assets[0].ProviderResourceID = name, "cn-hangzhou", "ecs", "i-interpreter-node"
	if _, err = s.Store.SaveSnapshot(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	turn := goalTurn(t, s, c.ID, "independent-history-word-name", "审计杭州名为 \""+name+"\" 的ECS入向，只运行规则")
	if turn.Proposal == nil || turn.Proposal.PlanID == "" || len(turn.Intent.Selection) != 1 || turn.Intent.Selection[0].ResourceID != "i-interpreter-node" {
		t.Fatalf("literal resource name was dispatched as a historical explanation: name=%q status=%s proposal=%v reply=%q", name, turn.Intent.Status, turn.Proposal, turn.Reply.PublicText)
	}
}

func TestIndependentLiteralBusinessWordsKeepOriginalMessageAndReplayIdentity(t *testing.T) {
	s, c, _, _ := rootWorkflowReviewReport(t)
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	const name = "确认Web服务"
	in.SnapshotVersion = "independent-business-word-name"
	in.Assets[0].Name, in.Assets[0].Region, in.Assets[0].ResourceType, in.Assets[0].ProviderResourceID = name, "cn-hangzhou", "ecs", "i-literal-business-name"
	if _, err = s.Store.SaveSnapshot(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	text := "审计杭州名为 \"" + name + "\" 的ECS入向，只运行规则"
	req := IntentTurnRequest{ClientTurnID: "independent-business-word-name", Text: text}
	turn, err := s.Turn(context.Background(), c.ID, "independent-business-word-name", req)
	if err != nil || turn.Business != nil || turn.Message == nil || turn.Message.PublicText != text || turn.Proposal == nil || turn.Proposal.PlanID == "" {
		t.Fatalf("literal business words changed local business or lost the visible original request: err=%v business=%v message=%v proposal=%v", err, turn.Business, turn.Message, turn.Proposal)
	}
	replay, err := s.Turn(context.Background(), c.ID, "independent-business-word-name", req)
	if err != nil || !replay.Replayed || replay.Message == nil || replay.Message.ID != turn.Message.ID || replay.Intent.Revision != turn.Intent.Revision {
		t.Fatal("original literal request could not recover its own receipt", err)
	}
	req.Text = "审计杭州名为 \"其他Web服务\" 的ECS入向，只运行规则"
	if _, err = s.Turn(context.Background(), c.ID, "independent-business-word-name", req); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("masked literal changed content was mistaken for the original request", err)
	}
}

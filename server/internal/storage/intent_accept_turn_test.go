package storage

import (
	"context"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func TestNaturalAcceptanceAndVisibleReceiptRollbackTogether(t *testing.T) {
	s, _, c, p := executionCase(t)
	ctx := context.Background()
	offer, e := s.SaveIntentRevision(ctx, SaveIntentTurn{ConversationID: c.ID, ExpectedConversationRevision: c.Revision, RequestKey: "natural-offer", RequestDigest: "natural-offer-digest", Text: "预览明确对象", Reply: "请核对完整计划", Intent: domain.ConversationIntent{Status: "ready_for_confirmation", Action: "audit", Mode: "rules_only", Proposal: &domain.ActionProposal{}}, Plan: &p})
	if e != nil || offer.Proposal == nil {
		t.Fatal(e)
	}
	req := SaveIntentTurn{LocalOnly: true, Acceptance: &IntentTurnAcceptance{ProposalID: offer.Proposal.ID, ConfigRevision: p.ConfigRevision, ExpectedRevision: offer.Intent.Revision}, ConversationID: c.ID, ExpectedConversationRevision: offer.Conversation.Revision, ExpectedIntentRevision: offer.Intent.Revision, RequestKey: "natural-confirm", RequestDigest: "natural-confirm-digest", Text: "确认按当前计划开始审计", Reply: "已接受原计划", Intent: offer.Intent}
	if e = s.db.Exec("CREATE TRIGGER reject_accept_receipt BEFORE INSERT ON conversation_intent_requests WHEN NEW.request_key='natural-confirm' BEGIN SELECT RAISE(ABORT,'ARTIFICIAL_SAVE_FAILURE'); END").Error; e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveIntentRevision(ctx, req); e == nil {
		t.Fatal("receipt failure was hidden")
	}
	jobs, e := s.ListJobs(ctx, c.ID, 0, 100)
	plan, pe := s.GetPlan(ctx, offer.Proposal.PlanID)
	if e != nil || pe != nil || len(jobs) != 0 || plan.ConsumedJobID != "" {
		t.Fatal("failed receipt committed an invisible execution", e, pe)
	}
	if e = s.db.Exec("DROP TRIGGER reject_accept_receipt").Error; e != nil {
		t.Fatal(e)
	}
	accepted, e := s.SaveIntentRevision(ctx, req)
	if e != nil || accepted.Job == nil || accepted.Job.PlanID != offer.Proposal.PlanID || accepted.Intent.Revision != offer.Intent.Revision || accepted.Intent.Status != "accepted" {
		t.Fatal("retry did not atomically consume original reviewed plan", e)
	}
	var acceptedEvent ConversationMessage
	if e = s.db.Raw("SELECT m.* FROM conversation_messages m JOIN conversation_turns t ON t.message_id=m.id WHERE t.job_id=?", accepted.Job.ID).Scan(&acceptedEvent).Error; e != nil || accepted.Message.Seq >= acceptedEvent.Seq || acceptedEvent.Seq >= accepted.Reply.Seq {
		t.Fatal("durable acceptance appeared before its user confirmation", e)
	}
	replay, e := s.SaveIntentRevision(ctx, req)
	if e != nil || !replay.Replayed || replay.Job == nil || replay.Job.ID != accepted.Job.ID || replay.Message.ID != accepted.Message.ID || replay.Reply.ID != accepted.Reply.ID {
		t.Fatal("same natural request created a second acceptance or receipt", e)
	}
}

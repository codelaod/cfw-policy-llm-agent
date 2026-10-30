package storage

import (
	"cfw-policy-llm-agent/server/internal/domain"
	"context"
	"encoding/json"
	"gorm.io/gorm"
)

type IntentAcceptResult struct {
	Job      Job  `json:"job"`
	Replayed bool `json:"replayed"`
}

// AcceptIntentProposal checks the current goal and consumes its plan within the
// same admission transaction. Idempotent replay precedes all freshness checks.
func (s *Store) AcceptIntentProposal(ctx context.Context, conversation, proposalID, key, configRevision string, revision int64) (out IntentAcceptResult, err error) {
	if !validKey(key) || proposalID == "" || revision < 1 {
		return out, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return acceptIntentProposal(tx, conversation, proposalID, key, configRevision, revision, &out)
	})
	return
}

func acceptIntentProposal(tx *gorm.DB, conversation, proposalID, key, configRevision string, revision int64, out *IntentAcceptResult) error {
	if !validKey(key) || proposalID == "" || revision < 1 {
		return ErrInputInvalid
	}
	var row struct {
		ConversationID, IntentID, Body, Status, PlanID string
		IntentRevision                                 int64
	}
	r := tx.Raw("SELECT * FROM action_proposals WHERE id=? AND conversation_id=?", proposalID, conversation).Scan(&row)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrNotFound
	}
	digest := requestDigest(struct {
		Conversation, ProposalID, PlanID string
		Revision                         int64
	}{conversation, proposalID, row.PlanID, revision})
	job, found, e := existingJob(tx, key, digest)
	if e != nil {
		return e
	}
	if found {
		out.Job = job
		out.Replayed = true
		return nil
	}
	intent, e := currentIntent(tx, conversation)
	if e != nil {
		return e
	}
	if row.Status != "ready" || row.IntentRevision != revision || intent.Revision != revision || intent.ID != row.IntentID || row.PlanID == "" {
		return ErrPlanStale
	}
	p, e := planRecord(tx, row.PlanID)
	if e != nil {
		return e
	}
	if p.ConversationID != conversation {
		return ErrInputInvalid
	}
	if e = validatePlanForConsumption(tx, p, []string{configRevision}); e != nil {
		return e
	}
	out.Job, _, e = createPlannedJob(tx, p, key, digest, MessageInput{})
	if e != nil {
		return e
	}
	var proposal domain.ActionProposal
	if e = json.Unmarshal([]byte(row.Body), &proposal); e != nil {
		return e
	}
	proposal.Status = "accepted"
	body, e := json.Marshal(proposal)
	if e != nil {
		return e
	}
	return tx.Exec("UPDATE action_proposals SET status='accepted',body=? WHERE id=? AND status='ready'", string(body), proposalID).Error
}

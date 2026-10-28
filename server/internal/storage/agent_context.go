package storage

import (
	"context"

	"gorm.io/gorm"
)

const AgentContextMessageLimit = 20

// AgentContextWindow is a read-only capture at the accepted turn position. It
// deliberately ignores the conversation's later current scope/archive state.
type AgentContextWindow struct {
	Messages          []ConversationMessage
	AcceptedMessageID string
	AcceptedSeq       int64
	TotalMessages     int
	ScopeMessages     int
	KnownLocalRefs    []string
}

func (s *Store) ReadAgentContextWindow(ctx context.Context, jobID string) (window AgentContextWindow, err error) {
	window.Messages = []ConversationMessage{}
	window.KnownLocalRefs = []string{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, jobID)
		if e != nil {
			return e
		}
		if !enum(job.Kind, "audit", "assistant_turn") || job.PlanID == "" {
			return ErrInputInvalid
		}
		plan, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		if plan.ConversationID != job.ConversationID || plan.ConsumedJobID != job.ID || plan.Kind != job.Kind || plan.PolicyScopeRef == "" || plan.PreparedInput.Scope.Ref != plan.PolicyScopeRef {
			return ErrInputInvalid
		}
		var turn ConversationTurn
		row := tx.Raw("SELECT * FROM conversation_turns WHERE job_id=?", jobID).Scan(&turn)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected != 1 || turn.ConversationID != job.ConversationID || turn.PlanID != plan.ID {
			return ErrInputInvalid
		}
		var accepted ConversationMessage
		row = tx.Raw("SELECT * FROM conversation_messages WHERE id=?", turn.MessageID).Scan(&accepted)
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected != 1 || accepted.ConversationID != job.ConversationID || accepted.JobID != job.ID || accepted.PolicyScopeRef != plan.PolicyScopeRef {
			return ErrInputInvalid
		}
		window.AcceptedMessageID = accepted.ID
		window.AcceptedSeq = accepted.Seq
		if e = tx.Raw("SELECT count(*) FROM conversation_messages WHERE conversation_id=? AND seq<=?", job.ConversationID, accepted.Seq).Scan(&window.TotalMessages).Error; e != nil {
			return e
		}
		if e = tx.Raw("SELECT count(*) FROM conversation_messages WHERE conversation_id=? AND policy_scope_ref=? AND seq<=?", job.ConversationID, plan.PolicyScopeRef, accepted.Seq).Scan(&window.ScopeMessages).Error; e != nil {
			return e
		}
		if e = tx.Raw("SELECT * FROM conversation_messages WHERE conversation_id=? AND policy_scope_ref=? AND seq<=? ORDER BY seq DESC LIMIT ?", job.ConversationID, plan.PolicyScopeRef, accepted.Seq, AgentContextMessageLimit).Scan(&window.Messages).Error; e != nil {
			return e
		}
		refs := map[string]bool{}
		add := func(id string) {
			if id != "" && !refs[id] {
				refs[id] = true
				window.KnownLocalRefs = append(window.KnownLocalRefs, id)
			}
		}
		for _, id := range []string{job.ID, job.ConversationID, plan.ID, plan.SnapshotID, plan.ContextReportID, accepted.ID} {
			add(id)
		}
		for _, message := range window.Messages {
			add(message.ID)
			add(message.JobID)
			add(message.RefID)
		}
		return nil
	})
	// Present oldest-to-newest only after the short read transaction has ended.
	for i, j := 0, len(window.Messages)-1; i < j; i, j = i+1, j-1 {
		window.Messages[i], window.Messages[j] = window.Messages[j], window.Messages[i]
	}
	return
}

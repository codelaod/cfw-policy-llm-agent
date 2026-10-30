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
		if plan.ConversationID != job.ConversationID || plan.ConsumedJobID != job.ID || plan.Kind != job.Kind || plan.PreparedInput.Scope.Ref != plan.PolicyScopeRef {
			return ErrInputInvalid
		}
		authority, e := batchAuthority(tx, job)
		if e != nil {
			return e
		}
		authorityPlan := plan
		if authority.ID != job.ID {
			authorityPlan, e = planRecord(tx, authority.PlanID)
			if e != nil {
				return e
			}
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
		if row.RowsAffected != 1 || accepted.ConversationID != job.ConversationID || accepted.JobID != authority.ID || accepted.PolicyScopeRef != authorityPlan.PolicyScopeRef {
			return ErrInputInvalid
		}
		window.AcceptedMessageID = accepted.ID
		window.AcceptedSeq = accepted.Seq
		if e = tx.Raw("SELECT count(*) FROM conversation_messages WHERE conversation_id=? AND seq<=?", job.ConversationID, accepted.Seq).Scan(&window.TotalMessages).Error; e != nil {
			return e
		}

		actual, e := executionPlanForJob(tx, job)
		if e != nil {
			return e
		}
		condition := "policy_scope_ref=?"
		arguments := []any{job.ConversationID, actual.PolicyScopeRef, accepted.Seq}
		if plan.Preparation != nil {
			if actual.SnapshotID == "" || actual.PolicyScopeRef == "" {
				return ErrInputNotSealed
			}
			// Empty scope is unresolved history, not an authorized partition.
			// Only the accepted operation and the exact goal revision that
			// produced this plan can join actual-partition public history.
			ids := []string{accepted.ID}
			var goalRows []struct{ MessageID, ReplyID string }
			if e = tx.Raw("SELECT json_extract(result_json,'$.message.id') AS message_id,json_extract(result_json,'$.reply.id') AS reply_id FROM conversation_intent_requests WHERE conversation_id=? AND json_extract(result_json,'$.proposal.plan_id')=?", job.ConversationID, plan.ID).Scan(&goalRows).Error; e != nil {
				return e
			}
			for _, row := range goalRows {
				if row.MessageID != "" {
					ids = append(ids, row.MessageID)
				}
				if row.ReplyID != "" {
					ids = append(ids, row.ReplyID)
				}
			}
			condition = "((policy_scope_ref=? AND policy_scope_ref<>'') OR id IN ?)"
			arguments = []any{job.ConversationID, actual.PolicyScopeRef, ids, accepted.Seq}
		}
		if e = tx.Raw("SELECT count(*) FROM conversation_messages WHERE conversation_id=? AND "+condition+" AND seq<=?", arguments...).Scan(&window.ScopeMessages).Error; e != nil {
			return e
		}
		arguments = append(arguments, AgentContextMessageLimit)
		if e = tx.Raw("SELECT * FROM conversation_messages WHERE conversation_id=? AND "+condition+" AND seq<=? ORDER BY seq DESC LIMIT ?", arguments...).Scan(&window.Messages).Error; e != nil {
			return e
		}
		refs := map[string]bool{}
		add := func(id string) {
			if id != "" && !refs[id] {
				refs[id] = true
				window.KnownLocalRefs = append(window.KnownLocalRefs, id)
			}
		}
		for _, id := range []string{job.ID, authority.ID, authorityPlan.ID, job.ConversationID, plan.ID, actual.SnapshotID, plan.ContextReportID, accepted.ID} {
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

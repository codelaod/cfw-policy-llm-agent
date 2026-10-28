package storage

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

// assistantDigest covers the stable user request, excluding later conversation,
// capability and business-head state. An accepted retry only observes its turn.
func assistantDigest(input ExecutionPlan, message MessageInput) string {
	return requestDigest(struct {
		ConversationID, SnapshotID, Scope, Mode string
		Request                                 json.RawMessage
		Message                                 MessageInput
		ContextReportID                         string `json:"context_report_id,omitempty"`
	}{input.ConversationID, input.SnapshotID, input.PolicyScopeRef, input.Mode, input.Request, message, input.ContextReportID})
}

// FindAssistantReplay is read-only. It must precede mutable capability, archive
// and scope checks at an entry point; no match grants no execution permission.
func (s *Store) FindAssistantReplay(ctx context.Context, input ExecutionPlan, message MessageInput) (job Job, saved ConversationMessage, found bool, err error) {
	message, err = normalizeMessage(message)
	if err != nil {
		return
	}
	if input.Kind != "assistant_turn" || message.Role != "user" || message.Origin != "user" || message.PolicyScopeRef != input.PolicyScopeRef {
		return job, saved, false, ErrInputInvalid
	}
	digest := assistantDigest(input, message)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		job, found, e = existingJob(tx, message.ClientMessageID, digest)
		if e != nil || !found {
			return e
		}
		if job.Kind != "assistant_turn" {
			return ErrConflict
		}
		var turn ConversationTurn
		r := tx.Raw("SELECT * FROM conversation_turns WHERE job_id=?", job.ID).Scan(&turn)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		r = tx.Raw("SELECT * FROM conversation_messages WHERE id=?", turn.MessageID).Scan(&saved)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	return
}

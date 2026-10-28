package storage

import "gorm.io/gorm"

// insertJobConversationMessage is for already accepted server work, including
// finalization after a user changes or archives the conversation. A JobID inside
// MessageInput alone grants no exception to ordinary conversation appends.
func insertJobConversationMessage(tx *gorm.DB, jobID string, input MessageInput) (message ConversationMessage, replayed bool, err error) {
	job, err := get(tx, jobID)
	if err != nil {
		return message, false, err
	}
	plan, err := planRecord(tx, job.PlanID)
	if err != nil {
		return message, false, err
	}
	if input.JobID != "" && input.JobID != jobID {
		return message, false, ErrInputInvalid
	}
	input.JobID = jobID
	return insertConversationMessageContext(tx, job.ConversationID, input, plan.ContextReportID, jobID)
}

func validateJobMessageContext(tx *gorm.DB, conversationID string, input MessageInput, jobID string) error {
	job, err := get(tx, jobID)
	if err != nil {
		return err
	}
	if job.Kind == "e0_probe" || job.ConversationID != conversationID || input.JobID != jobID || job.PlanID == "" {
		return ErrInputInvalid
	}
	plan, err := planRecord(tx, job.PlanID)
	if err != nil {
		return err
	}
	if plan.ConversationID != conversationID || plan.ID != job.PlanID || plan.ConsumedJobID != job.ID || plan.Kind != job.Kind || plan.PolicyScopeRef != input.PolicyScopeRef {
		return ErrInputInvalid
	}
	var turn ConversationTurn
	row := tx.Raw("SELECT * FROM conversation_turns WHERE job_id=?", jobID).Scan(&turn)
	if row.Error != nil {
		return row.Error
	}
	if row.RowsAffected != 1 || turn.ConversationID != conversationID || turn.PlanID != plan.ID {
		return ErrInputInvalid
	}
	var accepted ConversationMessage
	row = tx.Raw("SELECT * FROM conversation_messages WHERE id=?", turn.MessageID).Scan(&accepted)
	if row.Error != nil {
		return row.Error
	}
	if row.RowsAffected != 1 || accepted.ConversationID != conversationID || accepted.JobID != jobID || accepted.PolicyScopeRef != plan.PolicyScopeRef {
		return ErrInputInvalid
	}
	switch {
	case input.Role == "assistant" && input.Origin == "model":
		if !plan.EffectCeiling.AllowModelRequests || !enum(plan.Mode, "assistant", "rules_and_agent") {
			return ErrInputInvalid
		}
		if job.Status != "running" {
			return ErrJobTerminal
		}
	case input.Role == "event" && input.Origin == "application":
		if job.Status == "running" {
			return nil
		}
		if !terminal(job.Status) || input.ClientMessageID != "terminal_"+job.ID || input.RefKind != "job" || input.RefID != job.ID {
			return ErrJobTerminal
		}
	default:
		return ErrInputInvalid
	}
	return nil
}

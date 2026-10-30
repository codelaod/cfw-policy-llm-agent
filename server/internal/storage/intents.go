package storage

import (
	"context"
	"encoding/json"
	"strings"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

type IntentTurnResult struct {
	Group *ResourceGroupResult `json:"group,omitempty"`

	Business *ConversationBusinessResult `json:"business,omitempty"`

	Conversation Conversation              `json:"conversation"`
	Message      *ConversationMessage      `json:"message"`
	Reply        *ConversationMessage      `json:"reply"`
	Intent       domain.ConversationIntent `json:"intent"`
	Proposal     *domain.ActionProposal    `json:"proposal,omitempty"`
	Job          *Job                      `json:"job,omitempty"`
	Replayed     bool                      `json:"replayed"`
}
type SaveIntentTurn struct {
	Acceptance *IntentTurnAcceptance
	Group      *ResourceGroupResult

	Business *ConversationBusinessResult

	LocalOnly                    bool
	RequestKey                   string
	RequestDigest                string
	ConversationID               string
	ExpectedConversationRevision int64
	ExpectedIntentRevision       int64
	Intent                       domain.ConversationIntent
	Text                         string
	Reply                        string
	Plan                         *ExecutionPlan
}

type IntentTurnAcceptance struct {
	ProposalID, ConfigRevision string
	ExpectedRevision           int64
}

func currentIntent(tx *gorm.DB, conversation string) (intent domain.ConversationIntent, err error) {
	var row struct{ Body string }
	r := tx.Raw("SELECT i.body FROM conversation_intents i JOIN conversation_intent_heads h ON h.intent_id=i.id AND h.revision=i.revision WHERE h.conversation_id=?", conversation).Scan(&row)
	if r.Error != nil {
		return intent, r.Error
	}
	if r.RowsAffected == 0 {
		return intent, ErrNotFound
	}
	err = json.Unmarshal([]byte(row.Body), &intent)
	return
}
func (s *Store) CurrentIntent(ctx context.Context, conversation string) (domain.ConversationIntent, error) {
	if _, err := s.GetConversation(ctx, conversation); err != nil {
		return domain.ConversationIntent{}, err
	}
	return currentIntent(s.db.WithContext(ctx), conversation)
}
func intentReplay(tx *gorm.DB, conversation, key, digest string) (result IntentTurnResult, found bool, err error) {
	var row struct{ ConversationID, RequestDigest, ResultJSON string }
	r := tx.Raw("SELECT * FROM conversation_intent_requests WHERE request_key=?", key).Scan(&row)
	if r.Error != nil {
		return result, false, r.Error
	}
	if r.RowsAffected == 0 {
		return result, false, nil
	}
	if row.ConversationID != conversation || row.RequestDigest != digest {
		return result, false, ErrConflict
	}
	if err = json.Unmarshal([]byte(row.ResultJSON), &result); err != nil {
		return result, false, err
	}
	result.Replayed = true
	return result, true, nil
}
func (s *Store) IntentReplay(ctx context.Context, conversation, key, digest string) (IntentTurnResult, bool, error) {
	return intentReplay(s.db.WithContext(ctx), conversation, key, digest)
}

// SaveIntentRevision commits both visible messages, the goal revision and a
// local proposal/plan together. No network or model work runs in this transaction.
func (s *Store) SaveIntentRevision(ctx context.Context, req SaveIntentTurn) (result IntentTurnResult, err error) {
	if !validKey(req.RequestKey) || !validKey(req.RequestDigest) || req.ConversationID == "" || strings.TrimSpace(req.Reply) == "" {
		return result, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var found bool
		var e error
		result, found, e = intentReplay(tx, req.ConversationID, req.RequestKey, req.RequestDigest)
		if e != nil || found {
			return e
		}
		c, e := getConversation(tx, req.ConversationID)
		if e != nil {
			return e
		}
		if c.Archived {
			return ErrConversationArchived
		}
		if c.Revision != req.ExpectedConversationRevision {
			return ErrVersionConflict
		}
		old, e := currentIntent(tx, c.ID)
		if e != nil && e != ErrNotFound {
			return e
		}
		if old.Revision != req.ExpectedIntentRevision {
			return ErrVersionConflict
		}
		if req.Acceptance != nil {
			if !req.LocalOnly || req.Plan != nil || req.Acceptance.ExpectedRevision != old.Revision {
				return ErrInputInvalid
			}
			if req.Text != "" {
				message, _, e := insertConversationMessage(tx, c.ID, MessageInput{ClientMessageID: "intent_user_" + requestDigest([]string{c.ID, req.RequestKey}), Role: "user", Origin: "user", PublicText: req.Text, PolicyScopeRef: c.PolicyScopeRef})
				if e != nil {
					return e
				}
				result.Message = &message
			}
			var accepted IntentAcceptResult
			if e = acceptIntentProposal(tx, c.ID, req.Acceptance.ProposalID, "chat_accept_"+requestDigest([]string{c.ID, req.RequestKey}), req.Acceptance.ConfigRevision, req.Acceptance.ExpectedRevision, &accepted); e != nil {
				return e
			}
			result.Job = &accepted.Job
		}
		intent := req.Intent
		if !req.LocalOnly {
			intent.ConversationID = c.ID
			intent.Revision = old.Revision + 1
			if old.ID != "" {
				intent.ID = old.ID
			} else {
				intent.ID, e = opaqueID("intent")
				if e != nil {
					return e
				}
			}
			if intent.Selection == nil {
				intent.Selection = []domain.Resource{}
			}
			if intent.Candidates == nil {
				intent.Candidates = []domain.Resource{}
			}
			// A new target invalidates previous unconsumed previews but never rewrites a
			// running task. The scope change and its resulting plan are one atomic write.
			// Every goal revision revokes its older unconsumed plans, including
			// changes that retain the same policy scope. Running jobs stay frozen.
			c.Revision++
			if e = tx.Exec("UPDATE conversations SET revision=?,updated_at=? WHERE id=?", c.Revision, now(), c.ID).Error; e != nil {
				return e
			}
			if req.Plan != nil {
				if req.Plan.ConversationID != c.ID {
					return ErrInputInvalid
				}
				if c.PolicyScopeRef != req.Plan.PolicyScopeRef {
					if req.Plan.PolicyScopeRef != "" {
						if e = localScope(tx, req.Plan.PolicyScopeRef); e != nil {
							return e
						}
					} else if req.Plan.Preparation == nil {
						return ErrInputInvalid
					}
					c.PolicyScopeRef = req.Plan.PolicyScopeRef
					if e = tx.Exec("UPDATE conversations SET policy_scope_ref=?,revision=?,updated_at=? WHERE id=?", c.PolicyScopeRef, c.Revision, now(), c.ID).Error; e != nil {
						return e
					}
				}
				input := *req.Plan
				input.ConversationRevision = c.Revision
				p, e := preparePlan(input, input.Kind == "assistant_turn")
				if e != nil {
					return e
				}
				if e = insertPlan(tx, p); e != nil {
					return e
				}
				if intent.Proposal == nil {
					return ErrInputInvalid
				}
				intent.Proposal.PlanID = p.ID
			}
			if e = tx.Exec("UPDATE action_proposals SET status='stale' WHERE conversation_id=? AND status='ready'", c.ID).Error; e != nil {
				return e
			}
			if intent.Proposal != nil {
				intent.Proposal.ID, e = opaqueID("proposal")
				if e != nil {
					return e
				}
				intent.Proposal.IntentRevision = intent.Revision
				if intent.Proposal.PlanID != "" {
					intent.Proposal.Status = "ready"
				} else {
					intent.Proposal.Status = "blocked"
				}
			}
			body, e := marshalBounded(intent, 256<<10)
			if e != nil {
				return e
			}
			if e = tx.Exec("INSERT INTO conversation_intents(id,conversation_id,revision,body,created_at) VALUES(?,?,?,?,?)", intent.ID, c.ID, intent.Revision, string(body), now()).Error; e != nil {
				return e
			}
			if e = tx.Exec("INSERT INTO conversation_intent_heads(conversation_id,intent_id,revision) VALUES(?,?,?) ON CONFLICT(conversation_id) DO UPDATE SET intent_id=excluded.intent_id,revision=excluded.revision", c.ID, intent.ID, intent.Revision).Error; e != nil {
				return e
			}
			if intent.Proposal != nil {
				b, e := marshalBounded(intent.Proposal, 32768)
				if e != nil {
					return e
				}
				if e = tx.Exec("INSERT INTO action_proposals(id,conversation_id,intent_id,intent_revision,body,status,plan_id) VALUES(?,?,?,?,?,?,?)", intent.Proposal.ID, c.ID, intent.ID, intent.Revision, string(b), intent.Proposal.Status, nullableConfirmationID(intent.Proposal.PlanID)).Error; e != nil {
					return e
				}
			}

		} else {
			intent = old
			if intent.Proposal != nil && intent.Proposal.PlanID != "" {
				p, e := planRecord(tx, intent.Proposal.PlanID)
				if e != nil {
					return e
				}
				if p.ConsumedJobID != "" {
					intent.Status = "accepted"
					intent.Proposal.Status = "accepted"
				}
			}
			if intent.ID == "" {
				intent = req.Intent
				intent.ConversationID = c.ID
				intent.Revision = 0
			}
		}
		if req.Text != "" && result.Message == nil {
			m, _, e := insertConversationMessage(tx, c.ID, MessageInput{ClientMessageID: "intent_user_" + requestDigest([]string{c.ID, req.RequestKey}), Role: "user", Origin: "user", PublicText: req.Text, PolicyScopeRef: c.PolicyScopeRef})
			if e != nil {
				return e
			}
			result.Message = &m
		}
		m, _, e := insertConversationMessage(tx, c.ID, MessageInput{ClientMessageID: "intent_reply_" + requestDigest([]string{c.ID, req.RequestKey}), Role: "assistant", Origin: "application", PublicText: req.Reply, PolicyScopeRef: c.PolicyScopeRef})
		if e != nil {
			return e
		}
		result.Reply = &m
		result.Conversation, e = getConversation(tx, c.ID)
		if e != nil {
			return e
		}
		result.Business = req.Business
		result.Group = req.Group
		result.Intent = intent
		result.Proposal = intent.Proposal
		encoded, e := marshalBounded(result, 512<<10)
		if e != nil {
			return e
		}
		return tx.Exec("INSERT INTO conversation_intent_requests(request_key,conversation_id,request_digest,result_json) VALUES(?,?,?,?)", req.RequestKey, c.ID, req.RequestDigest, string(encoded)).Error
	})
	return
}
func (s *Store) IntentProposal(ctx context.Context, conversation, id string, revision int64) (p domain.ActionProposal, err error) {
	var row struct {
		Body, Status, PlanID, IntentID string
		IntentRevision                 int64
	}
	r := s.db.WithContext(ctx).Raw("SELECT * FROM action_proposals WHERE id=? AND conversation_id=?", id, conversation).Scan(&row)
	if r.Error != nil {
		return p, r.Error
	}
	if r.RowsAffected != 1 {
		return p, ErrNotFound
	}
	intent, e := s.CurrentIntent(ctx, conversation)
	if e != nil {
		return p, e
	}
	if row.Status != "ready" || row.IntentRevision != revision || intent.Revision != revision || intent.ID != row.IntentID {
		return p, ErrPlanStale
	}
	err = json.Unmarshal([]byte(row.Body), &p)
	return
}

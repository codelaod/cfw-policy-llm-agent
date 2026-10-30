package storage

import (
	"context"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

func (s *Store) CreateConversationWithKey(ctx context.Context, title, scope, key string) (conversation Conversation, replayed bool, err error) {
	if strings.TrimSpace(title) == "" || len(title) > 240 || !utf8.ValidString(title) || key != "" && !validKey(key) {
		return conversation, false, ErrInputInvalid
	}
	digest := requestDigest([]string{title, scope})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if key != "" {
			var old struct{ RequestDigest, ConversationID string }
			r := tx.Raw("SELECT * FROM conversation_creation_requests WHERE request_key=?", key).Scan(&old)
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected > 0 {
				if old.RequestDigest != digest {
					return ErrConflict
				}
				var e error
				conversation, e = getConversation(tx, old.ConversationID)
				replayed = true
				return e
			}
		}
		if e := localScope(tx, scope); e != nil {
			return e
		}
		id, e := opaqueID("conversation")
		if e != nil {
			return e
		}
		at := now()
		conversation = Conversation{ID: id, Title: title, PolicyScopeRef: scope, Revision: 1, CreatedAt: at, UpdatedAt: at}
		if e = tx.Exec("INSERT INTO conversations(id,title,policy_scope_ref,revision,archived,last_seq,created_at,updated_at) VALUES(?,?,?,1,0,0,?,?)", id, title, scope, at, at).Error; e != nil {
			return e
		}
		if e = tx.Exec("UPDATE workspace_state SET current_conversation_id=?,revision=revision+1 WHERE id=1", id).Error; e != nil {
			return e
		}
		if key != "" {
			return tx.Exec("INSERT INTO conversation_creation_requests(request_key,request_digest,conversation_id) VALUES(?,?,?)", key, digest, id).Error
		}
		return nil
	})
	return
}

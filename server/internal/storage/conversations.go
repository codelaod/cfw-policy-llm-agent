package storage

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

var ErrConversationArchived = errors.New("conversation is archived")

type Conversation struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	PolicyScopeRef string `json:"policy_scope_ref"`
	Revision       int64  `json:"revision"`
	Archived       bool   `json:"archived"`
	LastSeq        int64  `json:"last_seq"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}
type ConversationPatch struct {
	ExpectedRevision int64   `json:"expected_revision"`
	Title            *string `json:"title,omitempty"`
	PolicyScopeRef   *string `json:"policy_scope_ref,omitempty"`
	Archived         *bool   `json:"archived,omitempty"`
}
type MessageInput struct {
	ClientMessageID string `json:"client_message_id"`
	Role            string `json:"role"`
	Origin          string `json:"origin,omitempty"`
	PublicText      string `json:"public_text"`
	PolicyScopeRef  string `json:"policy_scope_ref"`
	JobID           string `json:"job_id,omitempty"`
	RefKind         string `json:"ref_kind,omitempty"`
	RefID           string `json:"ref_id,omitempty"`
}
type ConversationMessage struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversation_id"`
	Seq             int64  `json:"seq"`
	Role            string `json:"role"`
	Origin          string `json:"origin"`
	PublicText      string `json:"public_text"`
	PolicyScopeRef  string `json:"policy_scope_ref"`
	JobID           string `json:"job_id,omitempty"`
	RefKind         string `json:"ref_kind,omitempty"`
	RefID           string `json:"ref_id,omitempty"`
	CreatedAt       string `json:"created_at"`
	ClientMessageID string `json:"-"`
	RequestDigest   string `json:"-"`
}
type ConversationMessagePage struct {
	Items   []ConversationMessage `json:"items"`
	NextSeq int64                 `json:"next_seq"`
	HasMore bool                  `json:"has_more"`
	LastSeq int64                 `json:"last_seq"`
}
type WorkspaceState struct {
	ID                    string `json:"id"`
	CurrentConversationID string `json:"current_conversation_id"`
	Revision              int64  `json:"revision"`
}

// This is called only by the explicit migration path. The application must not
// infer that opening an old database is authorization to change its schema.
func migrateCollaboration(tx *gorm.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS conversations(id TEXT PRIMARY KEY,title TEXT NOT NULL,policy_scope_ref TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),archived INTEGER NOT NULL CHECK(archived IN (0,1)),last_seq INTEGER NOT NULL DEFAULT 0 CHECK(last_seq>=0),created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS workspace_state(id INTEGER PRIMARY KEY CHECK(id=1),current_conversation_id TEXT REFERENCES conversations(id),revision INTEGER NOT NULL CHECK(revision>=0))`,
		`INSERT OR IGNORE INTO workspace_state(id,current_conversation_id,revision) VALUES(1,NULL,0)`,
		`CREATE TABLE IF NOT EXISTS conversation_messages(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),seq INTEGER NOT NULL CHECK(seq>0),role TEXT NOT NULL CHECK(role IN ('user','assistant','event')),origin TEXT NOT NULL CHECK(origin IN ('user','application','model')),public_text TEXT NOT NULL,policy_scope_ref TEXT NOT NULL,job_id TEXT NOT NULL DEFAULT '',ref_kind TEXT NOT NULL DEFAULT '',ref_id TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,client_message_id TEXT UNIQUE NOT NULL,request_digest TEXT NOT NULL,UNIQUE(conversation_id,seq))`,
		`CREATE INDEX IF NOT EXISTS conversation_message_scope ON conversation_messages(conversation_id,policy_scope_ref,seq)`,
		`CREATE TABLE IF NOT EXISTS profile_versions(id TEXT PRIMARY KEY,scope_key TEXT NOT NULL,asset_key TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),source_snapshot_id TEXT NOT NULL REFERENCES snapshots(id),confirmed_at TEXT NOT NULL,UNIQUE(id,scope_key,asset_key))`,
		`CREATE TABLE IF NOT EXISTS intent_versions(id TEXT PRIMARY KEY,scope_key TEXT NOT NULL,asset_key TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),source_snapshot_id TEXT NOT NULL REFERENCES snapshots(id),confirmed_at TEXT NOT NULL,UNIQUE(id,scope_key,asset_key))`,
		`CREATE TABLE IF NOT EXISTS asset_heads(scope_key TEXT NOT NULL,asset_key TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>=0),profile_version_id TEXT,intent_version_id TEXT,PRIMARY KEY(scope_key,asset_key),FOREIGN KEY(profile_version_id,scope_key,asset_key) REFERENCES profile_versions(id,scope_key,asset_key),FOREIGN KEY(intent_version_id,scope_key,asset_key) REFERENCES intent_versions(id,scope_key,asset_key))`,
		`CREATE TABLE IF NOT EXISTS profile_drafts(id TEXT PRIMARY KEY,snapshot_id TEXT NOT NULL REFERENCES snapshots(id),scope_key TEXT NOT NULL,policy_scope_ref TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),status TEXT NOT NULL CHECK(status IN ('draft','previewed','confirmed','rejected')),body TEXT NOT NULL CHECK(json_valid(body)),base_json TEXT NOT NULL CHECK(json_valid(base_json)),preview_id TEXT REFERENCES confirmation_previews(id),confirmation_id TEXT REFERENCES confirmation_actions(id),created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS confirmation_previews(id TEXT PRIMARY KEY,draft_id TEXT NOT NULL REFERENCES profile_drafts(id),draft_revision INTEGER NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS confirmation_actions(id TEXT PRIMARY KEY,draft_id TEXT NOT NULL REFERENCES profile_drafts(id),kind TEXT NOT NULL CHECK(kind IN ('confirm','reject')),request_key TEXT UNIQUE NOT NULL,request_digest TEXT NOT NULL,result_json TEXT NOT NULL CHECK(json_valid(result_json)),saved_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func localScope(tx *gorm.DB, scope string) error {
	if scope == "" {
		return nil
	}
	if len(scope) > 128 {
		return ErrInputInvalid
	}
	var count int64
	if err := tx.Raw("SELECT (SELECT count(*) FROM snapshots WHERE scope_ref=?) + (SELECT count(*) FROM configured_scope_catalog WHERE scope_ref=? AND active=1)", scope, scope).Scan(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
func getConversation(tx *gorm.DB, id string) (conversation Conversation, err error) {
	r := tx.Raw("SELECT * FROM conversations WHERE id=?", id).Scan(&conversation)
	if r.Error != nil {
		return conversation, r.Error
	}
	if r.RowsAffected == 0 {
		return conversation, ErrNotFound
	}
	return
}
func (s *Store) CreateConversation(ctx context.Context, title, scope string) (Conversation, error) {
	conversation, _, err := s.CreateConversationWithKey(ctx, title, scope, "")
	return conversation, err
}
func (s *Store) GetConversation(ctx context.Context, id string) (Conversation, error) {
	return getConversation(s.db.WithContext(ctx), id)
}
func (s *Store) ListConversations(ctx context.Context, offset, limit int) (items []Conversation, err error) {
	if err = pageBounds(offset, limit); err != nil {
		return
	}
	items = []Conversation{}
	err = s.db.WithContext(ctx).Raw("SELECT * FROM conversations ORDER BY updated_at DESC,id DESC LIMIT ? OFFSET ?", limit, offset).Scan(&items).Error
	return
}
func (s *Store) PatchConversation(ctx context.Context, id string, patch ConversationPatch) (conversation Conversation, err error) {
	if patch.ExpectedRevision < 1 || (patch.Title == nil && patch.PolicyScopeRef == nil && patch.Archived == nil) {
		return conversation, ErrInputInvalid
	}
	if patch.Title != nil && (strings.TrimSpace(*patch.Title) == "" || len(*patch.Title) > 240 || !utf8.ValidString(*patch.Title)) {
		return conversation, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		conversation, err = getConversation(tx, id)
		if err != nil {
			return err
		}
		if conversation.Revision != patch.ExpectedRevision {
			return ErrVersionConflict
		}
		if patch.PolicyScopeRef != nil {
			if err = localScope(tx, *patch.PolicyScopeRef); err != nil {
				return err
			}
			conversation.PolicyScopeRef = *patch.PolicyScopeRef
		}
		if patch.Title != nil {
			conversation.Title = *patch.Title
		}
		if patch.Archived != nil {
			conversation.Archived = *patch.Archived
		}
		conversation.Revision++
		conversation.UpdatedAt = now()
		r := tx.Exec("UPDATE conversations SET title=?,policy_scope_ref=?,archived=?,revision=?,updated_at=? WHERE id=? AND revision=?", conversation.Title, conversation.PolicyScopeRef, conversation.Archived, conversation.Revision, conversation.UpdatedAt, id, patch.ExpectedRevision)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrVersionConflict
		}
		if conversation.Archived {
			if err := tx.Exec("UPDATE workspace_state SET current_conversation_id=NULL,revision=revision+1 WHERE id=1 AND current_conversation_id=?", id).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return
}

func currentWorkspace(tx *gorm.DB) (state WorkspaceState, err error) {
	r := tx.Raw("SELECT COALESCE(current_conversation_id,'') AS current_conversation_id,revision FROM workspace_state WHERE id=1").Scan(&state)
	state.ID = "local"
	if r.Error != nil {
		return state, r.Error
	}
	if r.RowsAffected != 1 {
		return state, ErrInputInvalid
	}
	return
}
func (s *Store) CurrentWorkspace(ctx context.Context) (WorkspaceState, error) {
	return currentWorkspace(s.db.WithContext(ctx))
}
func (s *Store) SelectCurrentConversation(ctx context.Context, id string, expectedRevision int64) (state WorkspaceState, err error) {
	if expectedRevision < 0 || len(id) > 128 {
		return state, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		state, e = currentWorkspace(tx)
		if e != nil {
			return e
		}
		if state.Revision != expectedRevision {
			return ErrVersionConflict
		}
		if id != "" {
			conversation, e := getConversation(tx, id)
			if e != nil {
				return e
			}
			if conversation.Archived {
				return ErrConversationArchived
			}
		}
		if state.CurrentConversationID == id {
			return nil
		}
		r := tx.Exec("UPDATE workspace_state SET current_conversation_id=?,revision=revision+1 WHERE id=1 AND revision=?", nullableConfirmationID(id), expectedRevision)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrVersionConflict
		}
		state.CurrentConversationID = id
		state.Revision++
		return nil
	})
	return
}

func normalizeMessage(input MessageInput) (MessageInput, error) {
	if !validKey(input.ClientMessageID) || strings.TrimSpace(input.PublicText) == "" || len(input.PublicText) > 16<<10 || !utf8.ValidString(input.PublicText) || len(input.PolicyScopeRef) > 128 || len(input.JobID) > 128 || len(input.RefID) > 128 {
		return input, ErrInputInvalid
	}
	if !enum(input.Role, "user", "assistant", "event") {
		return input, ErrInputInvalid
	}
	if input.Origin == "" {
		if input.Role == "user" {
			input.Origin = "user"
		} else {
			input.Origin = "application"
		}
	}
	if !enum(input.Origin, "user", "application", "model") || (input.Role == "user" && input.Origin != "user") || (input.Role == "event" && input.Origin != "application") || (input.Role == "assistant" && input.Origin == "user") {
		return input, ErrInputInvalid
	}
	if !enum(input.RefKind, "", "job", "audit", "report", "confirmation", "draft", "guidance", "snapshot", "finding") || (input.RefKind == "") != (input.RefID == "") {
		return input, ErrInputInvalid
	}
	return input, nil
}

// insertConversationMessage uses the caller's transaction. Job admission must
// call this helper together with its plan/turn/task writes, never append a user
// message first and enqueue a task in a later transaction. A historic user
// scope requires a stored report and its frozen audit input; accepting a caller
// supplied flag here would let ordinary messages bypass the current scope.
func insertConversationMessage(tx *gorm.DB, conversationID string, input MessageInput, validatedReportContextID ...string) (message ConversationMessage, replayed bool, err error) {
	if len(validatedReportContextID) > 1 {
		return message, false, ErrInputInvalid
	}
	reportContextID := ""
	if len(validatedReportContextID) == 1 {
		reportContextID = validatedReportContextID[0]
		if len(reportContextID) > 128 {
			return message, false, ErrInputInvalid
		}
	}
	return insertConversationMessageContext(tx, conversationID, input, reportContextID, "")
}

// A job context is accepted only after its durable job/plan/turn graph and
// sender have been checked. Ordinary append callers never supply this context.
func insertConversationMessageContext(tx *gorm.DB, conversationID string, input MessageInput, reportContextID, jobContextID string) (message ConversationMessage, replayed bool, err error) {
	input, err = normalizeMessage(input)
	if err != nil {
		return
	}
	digest := requestDigest(struct {
		ConversationID string
		Input          MessageInput
	}{conversationID, input})
	if reportContextID != "" {
		digest = requestDigest(struct {
			ConversationID  string
			Input           MessageInput
			ReportContextID string
		}{conversationID, input, reportContextID})
	}
	var old ConversationMessage
	r := tx.Raw("SELECT * FROM conversation_messages WHERE client_message_id=?", input.ClientMessageID).Scan(&old)
	if r.Error != nil {
		return message, false, r.Error
	}
	if r.RowsAffected > 0 {
		if old.RequestDigest != digest {
			return message, false, ErrConflict
		}
		return old, true, nil
	}
	conversation, err := getConversation(tx, conversationID)
	if err != nil {
		return message, false, err
	}
	if jobContextID != "" {
		if err := validateJobMessageContext(tx, conversationID, input, jobContextID); err != nil {
			return message, false, err
		}
	}
	if conversation.Archived && jobContextID == "" {
		return message, false, ErrConversationArchived
	}
	if reportContextID != "" {
		report, e := reportRecord(tx, reportContextID)
		if e != nil {
			return message, false, e
		}
		run, e := auditRun(tx, report.AuditID)
		if e != nil {
			return message, false, e
		}
		if run.Input.Scope.Ref != input.PolicyScopeRef {
			return message, false, ErrInputInvalid
		}
	}
	if input.PolicyScopeRef != conversation.PolicyScopeRef && jobContextID == "" && !(input.Role == "user" && reportContextID != "") {
		return message, false, ErrInputInvalid
	}
	// Already accepted work has just validated its immutable job/plan/turn and
	// original scope. A later catalog deactivation cannot revoke its metadata
	// finalization; ordinary appends still require a currently known scope.
	if jobContextID == "" {
		if err = localScope(tx, input.PolicyScopeRef); err != nil {
			return message, false, err
		}
	}
	id, err := opaqueID("message")
	if err != nil {
		return message, false, err
	}
	message = ConversationMessage{ID: id, ConversationID: conversationID, Seq: conversation.LastSeq + 1, Role: input.Role, Origin: input.Origin, PublicText: input.PublicText, PolicyScopeRef: input.PolicyScopeRef, JobID: input.JobID, RefKind: input.RefKind, RefID: input.RefID, CreatedAt: now(), ClientMessageID: input.ClientMessageID, RequestDigest: digest}
	if err = tx.Exec("INSERT INTO conversation_messages(id,conversation_id,seq,role,origin,public_text,policy_scope_ref,job_id,ref_kind,ref_id,created_at,client_message_id,request_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", message.ID, conversationID, message.Seq, message.Role, message.Origin, message.PublicText, message.PolicyScopeRef, message.JobID, message.RefKind, message.RefID, message.CreatedAt, message.ClientMessageID, digest).Error; err != nil {
		return message, false, err
	}
	r = tx.Exec("UPDATE conversations SET last_seq=?,updated_at=? WHERE id=? AND last_seq=?", message.Seq, message.CreatedAt, conversationID, conversation.LastSeq)
	if r.Error != nil {
		return message, false, r.Error
	}
	if r.RowsAffected != 1 {
		return message, false, ErrVersionConflict
	}
	return message, false, nil
}
func (s *Store) AppendConversationMessage(ctx context.Context, id string, input MessageInput) (message ConversationMessage, replayed bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		message, replayed, e = insertConversationMessage(tx, id, input)
		return e
	})
	return
}
func (s *Store) GetConversationMessage(ctx context.Context, id string) (message ConversationMessage, err error) {
	r := s.db.WithContext(ctx).Raw("SELECT * FROM conversation_messages WHERE id=?", id).Scan(&message)
	if r.Error != nil {
		return message, r.Error
	}
	if r.RowsAffected == 0 {
		return message, ErrNotFound
	}
	return
}
func (s *Store) ConversationMessages(ctx context.Context, id string, after int64, limit int) (page ConversationMessagePage, err error) {
	if after < 0 || limit < 1 || limit > 100 {
		return page, ErrInputInvalid
	}
	page.Items = []ConversationMessage{}
	page.NextSeq = after
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		conversation, e := getConversation(tx, id)
		if e != nil {
			return e
		}
		if after > conversation.LastSeq {
			return ErrCursor
		}
		page.LastSeq = conversation.LastSeq
		if e = tx.Raw("SELECT * FROM conversation_messages WHERE conversation_id=? AND seq>? ORDER BY seq LIMIT ?", id, after, limit).Scan(&page.Items).Error; e != nil {
			return e
		}
		if len(page.Items) > 0 {
			page.NextSeq = page.Items[len(page.Items)-1].Seq
		}
		page.HasMore = page.NextSeq < page.LastSeq
		return nil
	})
	return
}
func (s *Store) MessagesForScope(ctx context.Context, id, scope string, limit int) (messages []ConversationMessage, err error) {
	if limit < 1 || limit > 100 || scope == "" {
		return nil, ErrInputInvalid
	}
	messages = []ConversationMessage{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := getConversation(tx, id); e != nil {
			return e
		}
		if e := localScope(tx, scope); e != nil {
			return e
		}
		return tx.Raw("SELECT * FROM conversation_messages WHERE conversation_id=? AND policy_scope_ref=? ORDER BY seq DESC LIMIT ?", id, scope, limit).Scan(&messages).Error
	})
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return
}

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

var (
	ErrPlanStale      = errors.New("execution plan is stale")
	ErrPlanConsumed   = errors.New("execution plan is already consumed")
	ErrJobTerminal    = errors.New("job no longer accepts execution events")
	ErrInputNotSealed = errors.New("job input is not sealed")
)

type EffectCeiling struct {
	AllowCloudReads    bool `json:"allow_cloud_reads"`
	AllowModelRequests bool `json:"allow_model_requests"`
	CloudHTTPAttempts  int  `json:"cloud_http_attempts"`
	ModelHTTPAttempts  int  `json:"model_http_attempts"`
}
type ExecutionPlan struct {
	ID                   string             `json:"id"`
	Kind                 string             `json:"kind"`
	ConversationID       string             `json:"conversation_id"`
	DataSourceID         string             `json:"data_source_id"`
	PolicyScopeRef       string             `json:"policy_scope_ref"`
	SnapshotID           string             `json:"snapshot_id,omitempty"`
	Mode                 string             `json:"mode"`
	ConversationRevision int64              `json:"conversation_revision"`
	CreatedAt            string             `json:"created_at"`
	ExpiresAt            string             `json:"expires_at"`
	ConsumedJobID        string             `json:"consumed_job_id,omitempty"`
	RecheckOfJobID       string             `json:"recheck_of_job_id,omitempty"`
	ContextReportID      string             `json:"context_report_id,omitempty"`
	EvaluationAt         string             `json:"evaluation_at,omitempty"`
	Request              json.RawMessage    `json:"request" gorm:"-"`
	EffectCeiling        EffectCeiling      `json:"effect_ceiling" gorm:"-"`
	BudgetRef            string             `json:"budget_ref"`
	PreparedInput        domain.Input       `json:"-" gorm:"-"`
	Heads                []AssetHeadVersion `json:"-" gorm:"-"`
	ConfigRevision       string             `json:"-"`
	Body                 string             `json:"-"`
	PreparedJSON         string             `json:"-"`
	HeadsJSON            string             `json:"-"`
}

type ConversationTurn struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	PlanID         string `json:"plan_id"`
	JobID          string `json:"job_id"`
	MessageID      string `json:"message_id"`
	ReplyMessageID string `json:"reply_message_id,omitempty"`
	State          string `json:"state"`
	CreatedAt      string `json:"created_at"`
}

func migrateExecution(tx *gorm.DB) error {
	var version int
	if err := tx.Raw("SELECT version FROM business_schema").Scan(&version).Error; err != nil {
		return err
	}
	if version == 2 || version == BusinessSchemaVersion {
		return migrateCollaboration(tx)
	}
	if version != 1 {
		return errors.New("unsupported business schema for explicit migration")
	}
	if err := migrateCollaboration(tx); err != nil {
		return err
	}
	// A deliberately explicit repair can restore metadata removed from an
	// otherwise complete v2 database; it must not rebuild and discard that data.
	var shape int64
	if err := tx.Raw("SELECT (SELECT count(*) FROM sqlite_master WHERE type='table' AND name='execution_plans') + (SELECT count(*) FROM sqlite_master WHERE type='table' AND name='conversation_turns') + (SELECT count(*) FROM pragma_table_info('e0_jobs') WHERE name='kind') + (SELECT count(*) FROM pragma_table_info('audit_runs') WHERE name='input_body')").Scan(&shape).Error; err != nil {
		return err
	}
	if shape == 4 {
		return setBusinessSchema2(tx)
	}
	statements := []string{
		`CREATE TABLE execution_plans(id TEXT PRIMARY KEY,kind TEXT NOT NULL CHECK(kind IN ('audit','collect','connection_check','assistant_turn')),conversation_id TEXT NOT NULL REFERENCES conversations(id),data_source_id TEXT NOT NULL,policy_scope_ref TEXT NOT NULL,snapshot_id TEXT REFERENCES snapshots(id),mode TEXT NOT NULL,conversation_revision INTEGER NOT NULL,created_at TEXT NOT NULL,expires_at TEXT NOT NULL,consumed_job_id TEXT,config_revision TEXT NOT NULL,budget_ref TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),prepared_json TEXT NOT NULL CHECK(json_valid(prepared_json)),heads_json TEXT NOT NULL CHECK(json_valid(heads_json)))`,
		`INSERT OR IGNORE INTO conversations(id,title,policy_scope_ref,revision,archived,last_seq,created_at,updated_at) SELECT conversation_id,'E0本机联调历史','',1,0,0,min(created_at),max(created_at) FROM e0_jobs GROUP BY conversation_id`,
		`ALTER TABLE e0_events RENAME TO legacy_e0_events`,
		`ALTER TABLE e0_jobs RENAME TO legacy_e0_jobs`,
		`CREATE TABLE e0_jobs(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),client_request_id TEXT UNIQUE NOT NULL,request_digest TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('e0_probe','audit','collect','connection_check','assistant_turn')),plan_id TEXT UNIQUE REFERENCES execution_plans(id),status TEXT NOT NULL CHECK(status IN ('queued','running','cancelling','completed','partial','failed','cancelled','interrupted')),view_seq INTEGER NOT NULL DEFAULT 0 CHECK(view_seq>=0),created_at TEXT NOT NULL,started_at TEXT NOT NULL DEFAULT '',finished_at TEXT NOT NULL DEFAULT '',queue_deadline TEXT NOT NULL,root_deadline TEXT NOT NULL,execution_deadline TEXT NOT NULL DEFAULT '',result_ref TEXT REFERENCES audit_runs(id),error_code TEXT NOT NULL DEFAULT '',budget_ref TEXT NOT NULL,sealed_input TEXT CHECK(sealed_input IS NULL OR json_valid(sealed_input)))`,
		`INSERT INTO e0_jobs(id,conversation_id,client_request_id,request_digest,kind,status,view_seq,created_at,started_at,finished_at,queue_deadline,root_deadline,budget_ref) SELECT id,conversation_id,client_request_id,'','e0_probe',status,view_seq,created_at,started_at,finished_at,created_at,created_at,'application-default' FROM legacy_e0_jobs`,
		`CREATE TABLE e0_events(job_id TEXT NOT NULL REFERENCES e0_jobs(id),seq INTEGER NOT NULL CHECK(seq>0),schema_version INTEGER NOT NULL CHECK(schema_version=1),type TEXT NOT NULL,occurred_at TEXT NOT NULL,payload TEXT NOT NULL CHECK(json_valid(payload)),PRIMARY KEY(job_id,seq))`,
		`INSERT INTO e0_events SELECT * FROM legacy_e0_events`,
		`DROP TABLE legacy_e0_events`,
		`DROP TABLE legacy_e0_jobs`,
		`CREATE UNIQUE INDEX e0_one_active ON e0_jobs(conversation_id) WHERE status IN ('queued','running','cancelling')`,
		`CREATE TABLE conversation_turns(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),plan_id TEXT UNIQUE NOT NULL REFERENCES execution_plans(id),job_id TEXT UNIQUE NOT NULL REFERENCES e0_jobs(id),message_id TEXT UNIQUE NOT NULL REFERENCES conversation_messages(id),reply_message_id TEXT REFERENCES conversation_messages(id),state TEXT NOT NULL CHECK(state IN ('queued','running','completed','partial','failed','cancelled','interrupted')),created_at TEXT NOT NULL)`,
		`ALTER TABLE audit_runs ADD COLUMN input_body TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(input_body))`,
		`UPDATE audit_runs SET input_body=(SELECT body FROM snapshots WHERE snapshots.id=audit_runs.snapshot_id)`,
		`DROP TABLE business_schema`,
		`CREATE TABLE business_schema(version INTEGER PRIMARY KEY CHECK(version=2))`,
		`INSERT INTO business_schema VALUES(2)`,
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func setBusinessSchema2(tx *gorm.DB) error {
	for _, q := range []string{"DROP TABLE business_schema", "CREATE TABLE business_schema(version INTEGER PRIMARY KEY CHECK(version=2))", "INSERT INTO business_schema VALUES(2)"} {
		if err := tx.Exec(q).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateEffects(kind string, e EffectCeiling) error {
	if e.CloudHTTPAttempts < 0 || e.CloudHTTPAttempts > 100 || e.ModelHTTPAttempts < 0 || e.ModelHTTPAttempts > 6 {
		return ErrInputInvalid
	}
	if (!e.AllowCloudReads && e.CloudHTTPAttempts != 0) || (!e.AllowModelRequests && e.ModelHTTPAttempts != 0) {
		return ErrInputInvalid
	}
	if kind == "assistant_turn" && (e.AllowCloudReads || e.CloudHTTPAttempts != 0 || e.ModelHTTPAttempts > 4) {
		return ErrInputInvalid
	}
	if (kind == "collect" || kind == "connection_check") && (e.AllowModelRequests || e.ModelHTTPAttempts != 0) {
		return ErrInputInvalid
	}
	if kind == "connection_check" && e.CloudHTTPAttempts > 6 {
		return ErrInputInvalid
	}
	return nil
}

func preparePlan(p ExecutionPlan, internal bool) (ExecutionPlan, error) {
	if !enum(p.Kind, "audit", "collect", "connection_check", "assistant_turn") || (!internal && p.Kind == "assistant_turn") || p.ConversationID == "" || p.PolicyScopeRef == "" || p.DataSourceID == "" || p.ConversationRevision < 1 || len(p.ConfigRevision) > 128 {
		return p, ErrInputInvalid
	}
	if p.Kind == "audit" && !enum(p.Mode, "rules_only", "rules_and_agent") {
		return p, ErrInputInvalid
	}
	if len(p.ContextReportID) > 128 || (p.ContextReportID != "" && p.Kind != "assistant_turn") || (p.Kind == "assistant_turn" && p.Mode != "assistant") {
		return p, ErrInputInvalid
	}
	if err := validateEffects(p.Kind, p.EffectCeiling); err != nil {
		return p, err
	}
	if len(p.Request) == 0 {
		p.Request = json.RawMessage(`{}`)
	}
	if !json.Valid(p.Request) || len(p.Request) > 32<<10 {
		return p, ErrInputInvalid
	}
	if p.ID != "" || p.ConsumedJobID != "" {
		return p, ErrInputInvalid
	}
	var err error
	p.ID, err = opaqueID("plan")
	if err != nil {
		return p, err
	}
	at := time.Now().UTC()
	p.CreatedAt = at.Format(time.RFC3339Nano)
	p.ExpiresAt = at.Add(5 * time.Minute).Format(time.RFC3339Nano)
	if p.BudgetRef == "" {
		p.BudgetRef = "application-default"
	}
	if p.EvaluationAt != "" {
		if _, err = time.Parse(time.RFC3339Nano, p.EvaluationAt); err != nil {
			return p, ErrInputInvalid
		}
	}
	prepared, err := marshalBounded(p.PreparedInput, 5<<20)
	if err != nil {
		return p, err
	}
	p.PreparedJSON = string(prepared)
	if p.Heads == nil {
		p.Heads = []AssetHeadVersion{}
	}
	heads, err := marshalBounded(p.Heads, 64<<10)
	if err != nil {
		return p, err
	}
	p.HeadsJSON = string(heads)
	body, err := marshalBounded(p, 128<<10)
	if err != nil {
		return p, err
	}
	p.Body = string(body)
	return p, nil
}

// validateReportPlan binds an assistant explanation to the report's original
// immutable input. Local heads and the conversation's newer selected scope do
// not replace this explicit historical context.
func validateReportPlan(tx *gorm.DB, p ExecutionPlan) error {
	if p.ContextReportID == "" || p.Kind != "assistant_turn" || p.SnapshotID == "" || len(p.Heads) != 0 {
		return ErrInputInvalid
	}
	report, err := reportRecord(tx, p.ContextReportID)
	if err != nil {
		return err
	}
	run, err := auditRun(tx, report.AuditID)
	if err != nil {
		return err
	}
	if p.SnapshotID != run.SnapshotID || p.PolicyScopeRef != run.Input.Scope.Ref || p.EvaluationAt != run.Input.EvaluationAt || !reflect.DeepEqual(p.PreparedInput, run.Input) {
		return ErrInputInvalid
	}
	return nil
}

func insertPlan(tx *gorm.DB, p ExecutionPlan) error {
	conversation, err := getConversation(tx, p.ConversationID)
	if err != nil {
		return err
	}
	if conversation.Archived {
		return ErrConversationArchived
	}
	if conversation.Revision != p.ConversationRevision {
		return ErrPlanStale
	}
	if p.ContextReportID != "" {
		if err = validateReportPlan(tx, p); err != nil {
			return err
		}
	} else {
		if conversation.PolicyScopeRef != p.PolicyScopeRef {
			return ErrPlanStale
		}
		if err = localScope(tx, p.PolicyScopeRef); err != nil {
			return err
		}
		if p.SnapshotID != "" {
			source, err := snapshot(tx, p.SnapshotID)
			if err != nil {
				return err
			}
			if source.ScopeRef != p.PolicyScopeRef || !validResolvedConfiguration(source.Input, p.PreparedInput) {
				return ErrInputInvalid
			}
			if err = checkHeadVersions(tx, p.SnapshotID, p.Heads); err != nil {
				return err
			}
			expected, heads, e := preparedSelection(tx, p.SnapshotID, p.PreparedInput.Selection)
			if e != nil {
				return e
			}
			expected.EvaluationAt = p.PreparedInput.EvaluationAt
			if !reflect.DeepEqual(expected, p.PreparedInput) || !reflect.DeepEqual(heads, p.Heads) {
				return ErrInputInvalid
			}
		} else if p.Kind == "audit" || p.Kind == "assistant_turn" {
			return ErrInputInvalid
		}
	}
	var snapshotID any
	if p.SnapshotID != "" {
		snapshotID = p.SnapshotID
	}
	return tx.Exec("INSERT INTO execution_plans(id,kind,conversation_id,data_source_id,policy_scope_ref,snapshot_id,mode,conversation_revision,created_at,expires_at,config_revision,budget_ref,body,prepared_json,heads_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", p.ID, p.Kind, p.ConversationID, p.DataSourceID, p.PolicyScopeRef, snapshotID, p.Mode, p.ConversationRevision, p.CreatedAt, p.ExpiresAt, p.ConfigRevision, p.BudgetRef, p.Body, p.PreparedJSON, p.HeadsJSON).Error
}
func (s *Store) SavePlan(ctx context.Context, input ExecutionPlan) (p ExecutionPlan, err error) {
	p, err = preparePlan(input, false)
	if err != nil {
		return
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return insertPlan(tx, p) })
	return
}
func planRecord(tx *gorm.DB, id string) (p ExecutionPlan, err error) {
	var row ExecutionPlan
	r := tx.Raw("SELECT * FROM execution_plans WHERE id=?", id).Scan(&row)
	if r.Error != nil {
		return p, r.Error
	}
	if r.RowsAffected == 0 {
		return p, ErrNotFound
	}
	if err = json.Unmarshal([]byte(row.Body), &p); err != nil {
		return
	}
	p.Body = row.Body
	p.PreparedJSON = row.PreparedJSON
	p.HeadsJSON = row.HeadsJSON
	p.ConfigRevision = row.ConfigRevision
	p.ConsumedJobID = row.ConsumedJobID
	p.CreatedAt = row.CreatedAt
	p.ExpiresAt = row.ExpiresAt
	if err = json.Unmarshal([]byte(row.PreparedJSON), &p.PreparedInput); err != nil {
		return
	}
	err = json.Unmarshal([]byte(row.HeadsJSON), &p.Heads)
	return
}
func (s *Store) GetPlan(ctx context.Context, id string) (ExecutionPlan, error) {
	return planRecord(s.db.WithContext(ctx), id)
}

func timingFor(kind string) (time.Duration, time.Duration) {
	switch kind {
	case "assistant_turn":
		return 60 * time.Second, 90 * time.Second
	case "connection_check":
		return 60 * time.Second, 30 * time.Second
	case "collect":
		return 120 * time.Second, 90 * time.Second
	case "e0_probe":
		return 120 * time.Second, 30 * time.Second
	default:
		return 120 * time.Second, 240 * time.Second
	}
}
func queueAdmission(tx *gorm.DB, conversationID string) error {
	var count int64
	if err := tx.Raw("SELECT count(*) FROM e0_jobs WHERE conversation_id=? AND status IN ('queued','running','cancelling')", conversationID).Scan(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrBusy
	}
	if err := tx.Raw("SELECT count(*) FROM e0_jobs WHERE status='queued'").Scan(&count).Error; err != nil {
		return err
	}
	if count >= 10 {
		return ErrCapacity
	}
	return nil
}
func existingJob(tx *gorm.DB, key, digest string) (job Job, found bool, err error) {
	r := tx.Raw("SELECT * FROM e0_jobs WHERE client_request_id=?", key).Scan(&job)
	if r.Error != nil {
		return job, false, r.Error
	}
	if r.RowsAffected == 0 {
		return job, false, nil
	}
	if job.RequestDigest != digest {
		return job, true, ErrConflict
	}
	return job, true, nil
}
func validatePlanForConsumption(tx *gorm.DB, p ExecutionPlan, config []string) error {
	created, e := time.Parse(time.RFC3339Nano, p.CreatedAt)
	expires, e2 := time.Parse(time.RFC3339Nano, p.ExpiresAt)
	at := time.Now().UTC()
	if e != nil || e2 != nil || at.Before(created) || !at.Before(expires) {
		return ErrPlanStale
	}
	if p.ConsumedJobID != "" {
		return ErrPlanConsumed
	}
	if len(config) > 0 && p.ConfigRevision != config[0] {
		return ErrPlanStale
	}
	conversation, err := getConversation(tx, p.ConversationID)
	if err != nil {
		return err
	}
	if conversation.Archived {
		return ErrConversationArchived
	}
	if conversation.Revision != p.ConversationRevision {
		return ErrPlanStale
	}
	if p.ContextReportID != "" {
		if err = validateReportPlan(tx, p); err != nil {
			return err
		}
	} else {
		if conversation.PolicyScopeRef != p.PolicyScopeRef {
			return ErrPlanStale
		}
		if p.SnapshotID != "" {
			if err = checkHeadVersions(tx, p.SnapshotID, p.Heads); err != nil {
				if errors.Is(err, ErrVersionConflict) {
					return ErrPlanStale
				}
				return err
			}
		}
	}
	return queueAdmission(tx, p.ConversationID)
}
func createPlannedJob(tx *gorm.DB, p ExecutionPlan, key, digest string, message MessageInput) (job Job, saved ConversationMessage, err error) {
	id, err := opaqueID("job")
	if err != nil {
		return job, saved, err
	}
	at := time.Now().UTC()
	queue, execution := timingFor(p.Kind)
	job = Job{ID: id, ConversationID: p.ConversationID, ClientRequestID: key, RequestDigest: digest, Kind: p.Kind, PlanID: p.ID, Status: "queued", CreatedAt: at.Format(time.RFC3339Nano), QueueDeadline: at.Add(queue).Format(time.RFC3339Nano), RootDeadline: at.Add(queue + execution).Format(time.RFC3339Nano), BudgetRef: p.BudgetRef}
	if err = tx.Exec("INSERT INTO e0_jobs(id,conversation_id,client_request_id,request_digest,kind,plan_id,status,created_at,queue_deadline,root_deadline,budget_ref) VALUES(?,?,?,?,?,?,?,?,?,?,?)", id, job.ConversationID, key, digest, p.Kind, p.ID, "queued", job.CreatedAt, job.QueueDeadline, job.RootDeadline, job.BudgetRef).Error; err != nil {
		return
	}
	message.JobID = id
	if message.Role == "" {
		message = MessageInput{ClientMessageID: "operation_" + requestDigest([]string{key, p.ID}), Role: "event", Origin: "application", PublicText: "已明确启动本次有界任务；执行结果将保存到当前会话。", PolicyScopeRef: p.PolicyScopeRef, JobID: id, RefKind: "job", RefID: id}
	}
	if saved, _, err = insertConversationMessage(tx, p.ConversationID, message, p.ContextReportID); err != nil {
		return
	}
	turnID, e := opaqueID("turn")
	if e != nil {
		return job, saved, e
	}
	if err = tx.Exec("INSERT INTO conversation_turns(id,conversation_id,plan_id,job_id,message_id,state,created_at) VALUES(?,?,?,?,?,'queued',?)", turnID, p.ConversationID, p.ID, id, saved.ID, job.CreatedAt).Error; err != nil {
		return
	}
	if err = tx.Exec("UPDATE execution_plans SET consumed_job_id=? WHERE id=? AND consumed_job_id IS NULL", id, p.ID).Error; err != nil {
		return
	}
	err = appendEvent(tx, &job, "job_status", map[string]any{"status": "queued", "phase": "L0", "message_ref": saved.ID})
	return
}
func (s *Store) EnqueuePlan(ctx context.Context, planID, key string, configRevision ...string) (job Job, replayed bool, err error) {
	if !validKey(key) || planID == "" {
		return job, false, ErrInputInvalid
	}
	digest := requestDigest(struct{ PlanID string }{planID})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var found bool
		var e error
		job, found, e = existingJob(tx, key, digest)
		if e != nil {
			return e
		}
		if found {
			replayed = true
			return nil
		}
		plan, e := planRecord(tx, planID)
		if e != nil {
			return e
		}
		if plan.Kind == "assistant_turn" {
			return ErrInputInvalid
		}
		if e = validatePlanForConsumption(tx, plan, configRevision); e != nil {
			return e
		}
		job, _, e = createPlannedJob(tx, plan, key, digest, MessageInput{})
		return e
	})
	return
}
func (s *Store) EnqueueAssistant(ctx context.Context, input ExecutionPlan, message MessageInput) (job Job, saved ConversationMessage, replayed bool, err error) {
	message, err = normalizeMessage(message)
	if err != nil {
		return
	}
	if input.Kind != "assistant_turn" || message.Role != "user" || message.Origin != "user" || message.PolicyScopeRef != input.PolicyScopeRef {
		return job, saved, false, ErrInputInvalid
	}
	digest := assistantDigest(input, message)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var found bool
		var e error
		job, found, e = existingJob(tx, message.ClientMessageID, digest)
		if e != nil {
			return e
		}
		if found {
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
			replayed = true
			return nil
		}
		plan, e := preparePlan(input, true)
		if e != nil {
			return e
		}
		if e = insertPlan(tx, plan); e != nil {
			return e
		}
		if e = validatePlanForConsumption(tx, plan, nil); e != nil {
			return e
		}
		job, saved, e = createPlannedJob(tx, plan, message.ClientMessageID, digest, message)
		return e
	})
	return
}

func validResolvedConfiguration(base, in domain.Input) bool {
	if base.SchemaVersion != in.SchemaVersion || base.SnapshotVersion != in.SnapshotVersion || base.SourceKind != in.SourceKind || base.CollectedAt != in.CollectedAt || base.NormalizerVersion != in.NormalizerVersion || base.KernelVersion != in.KernelVersion || !reflect.DeepEqual(base.Scope, in.Scope) || !reflect.DeepEqual(base.Semantics, in.Semantics) || !reflect.DeepEqual(base.Completeness, in.Completeness) || !reflect.DeepEqual(base.Protection, in.Protection) || !reflect.DeepEqual(base.Policies, in.Policies) || !reflect.DeepEqual(base.AddressRefs, in.AddressRefs) || !reflect.DeepEqual(base.PortRefs, in.PortRefs) || base.Baseline.Version != in.Baseline.Version || !reflect.DeepEqual(base.Baseline.EnabledChecks, in.Baseline.EnabledChecks) || !reflect.DeepEqual(base.Baseline.RiskPorts, in.Baseline.RiskPorts) {
		return false
	}
	selected := map[string]bool{}
	for _, id := range base.Selection.AssetIDs {
		selected[id] = true
	}
	if in.Selection.Kind == "gateway_policy" {
		return in.Scope.Boundary == "nat" && len(in.Assets) == 0 && len(in.Selection.AssetIDs) == 0
	}
	if in.Selection.Kind != "assets" || len(in.Selection.AssetIDs) == 0 || len(in.Selection.AssetIDs) != len(in.Assets) {
		return false
	}
	known := map[string]domain.Asset{}
	for _, asset := range base.Assets {
		known[asset.ID] = asset
	}
	seen := map[string]bool{}
	for _, id := range in.Selection.AssetIDs {
		if !selected[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, asset := range in.Assets {
		old, ok := known[asset.ID]
		if !ok || !seen[asset.ID] || asset.ScopeRef != old.ScopeRef || asset.Address != old.Address || asset.AddressObserved != old.AddressObserved || !reflect.DeepEqual(asset.Association, old.Association) {
			return false
		}
	}
	return true
}

func preparedSelection(tx *gorm.DB, snapshotID string, selection domain.Selection) (in domain.Input, heads []AssetHeadVersion, err error) {
	in, allHeads, err := freezeInput(tx, snapshotID)
	if err != nil {
		return in, nil, err
	}
	base, err := snapshot(tx, snapshotID)
	if err != nil {
		return in, nil, err
	}
	if selection.Kind == "gateway_policy" {
		if in.Scope.Boundary != "nat" || len(selection.AssetIDs) != 0 {
			return in, nil, ErrInputInvalid
		}
		in.Selection = selection
		in.Assets = []domain.Asset{}
		in.Baseline.Exceptions = []domain.Exception{}
		in.ProfileVersion = base.Input.ProfileVersion
		in.IntentVersion = base.Input.IntentVersion
		return in, []AssetHeadVersion{}, nil
	}
	if selection.Kind != "assets" || len(selection.AssetIDs) == 0 || len(selection.AssetIDs) > 50 {
		return in, nil, ErrInputInvalid
	}
	selected := map[string]bool{}
	for _, id := range selection.AssetIDs {
		if id == "" || selected[id] {
			return in, nil, ErrInputInvalid
		}
		selected[id] = true
	}
	assets := []domain.Asset{}
	for _, asset := range in.Assets {
		if selected[asset.ID] {
			assets = append(assets, asset)
		}
	}
	if len(assets) != len(selected) {
		return in, nil, ErrInputInvalid
	}
	heads = []AssetHeadVersion{}
	profiles, intents := []string{}, []string{}
	for _, head := range allHeads {
		if selected[head.AssetID] {
			heads = append(heads, head)
			if head.ProfileVersionID != "" {
				profiles = append(profiles, head.AssetID+"/"+head.ProfileVersionID)
			}
			if head.IntentVersionID != "" {
				intents = append(intents, head.AssetID+"/"+head.IntentVersionID)
			}
		}
	}
	exceptions := []domain.Exception{}
	for _, exception := range in.Baseline.Exceptions {
		if selected[exception.SubjectRef] {
			exceptions = append(exceptions, exception)
		}
	}
	in.Selection = domain.Selection{Kind: "assets", AssetIDs: append([]string(nil), selection.AssetIDs...)}
	in.Assets = assets
	in.Baseline.Exceptions = exceptions
	in.ProfileVersion = base.Input.ProfileVersion
	in.IntentVersion = base.Input.IntentVersion
	if len(profiles) > 0 {
		sort.Strings(profiles)
		in.ProfileVersion = "profile-set_" + requestDigest(struct {
			Base     string
			Versions []string
		}{in.ProfileVersion, profiles})[:32]
	}
	if len(intents) > 0 {
		sort.Strings(intents)
		in.IntentVersion = "intent-set_" + requestDigest(struct {
			Base     string
			Versions []string
		}{in.IntentVersion, intents})[:32]
	}
	return
}
func (s *Store) FreezeSelection(ctx context.Context, id string, selection domain.Selection) (in domain.Input, heads []AssetHeadVersion, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var e error; in, heads, e = preparedSelection(tx, id, selection); return e })
	return
}
func (s *Store) SealInput(ctx context.Context, jobID string, input domain.Input) (sealed domain.Input, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, jobID)
		if e != nil {
			return e
		}
		if terminal(job.Status) || job.Status == "cancelling" {
			return ErrJobTerminal
		}
		if job.Status != "running" || job.PlanID == "" {
			return ErrInputInvalid
		}
		plan, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		var stored struct{ SealedInput *string }
		if e = tx.Raw("SELECT sealed_input FROM e0_jobs WHERE id=?", jobID).Scan(&stored).Error; e != nil {
			return e
		}
		if stored.SealedInput != nil {
			if e = json.Unmarshal([]byte(*stored.SealedInput), &sealed); e != nil {
				return e
			}
			probe := input
			if probe.EvaluationAt == "" {
				probe.EvaluationAt = sealed.EvaluationAt
			}
			if !reflect.DeepEqual(probe, sealed) {
				return ErrConflict
			}
			return nil
		}
		if input.EvaluationAt == "" {
			input.EvaluationAt = now()
		}
		if plan.EvaluationAt != "" && input.EvaluationAt != plan.EvaluationAt {
			return ErrInputInvalid
		}
		expected := plan.PreparedInput
		expected.EvaluationAt = input.EvaluationAt
		if !reflect.DeepEqual(expected, input) {
			return ErrInputInvalid
		}
		if _, e = time.Parse(time.RFC3339Nano, input.EvaluationAt); e != nil {
			return ErrInputInvalid
		}
		body, e := marshalBounded(input, 5<<20)
		if e != nil {
			return e
		}
		r := tx.Exec("UPDATE e0_jobs SET sealed_input=? WHERE id=? AND sealed_input IS NULL AND status='running'", string(body), jobID)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrJobTerminal
		}
		if e = json.Unmarshal(body, &sealed); e != nil {
			return e
		}
		return appendEvent(tx, &job, "stage_changed", map[string]any{"phase": "L1", "stage": "input_sealed"})
	})
	return
}
func jobInput(tx *gorm.DB, id string) (in domain.Input, err error) {
	var row struct{ SealedInput *string }
	r := tx.Raw("SELECT sealed_input FROM e0_jobs WHERE id=?", id).Scan(&row)
	if r.Error != nil {
		return in, r.Error
	}
	if r.RowsAffected == 0 {
		return in, ErrNotFound
	}
	if row.SealedInput == nil {
		return in, ErrInputNotSealed
	}
	err = json.Unmarshal([]byte(*row.SealedInput), &in)
	return
}
func (s *Store) GetJobInput(ctx context.Context, id string) (domain.Input, error) {
	return jobInput(s.db.WithContext(ctx), id)
}
func (s *Store) ListJobs(ctx context.Context, conversationID string, offset, limit int) (jobs []Job, err error) {
	if err = pageBounds(offset, limit); err != nil {
		return
	}
	jobs = []Job{}
	query := "SELECT * FROM e0_jobs"
	args := []any{}
	if conversationID != "" {
		query += " WHERE conversation_id=?"
		args = append(args, conversationID)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	err = s.db.WithContext(ctx).Raw(query, args...).Scan(&jobs).Error
	return
}
func (s *Store) AppendJobEvent(ctx context.Context, id, typ string, payload map[string]any) error {
	if !enum(typ, "stage_changed", "tool_started", "tool_finished") {
		return ErrInputInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, id)
		if e != nil {
			return e
		}
		if terminal(job.Status) || job.Status == "cancelling" {
			return ErrJobTerminal
		}
		return appendEvent(tx, &job, typ, payload)
	})
}
func (s *Store) AttachResultAndEvent(ctx context.Context, id, auditID, typ string, payload map[string]any) error {
	if typ != "findings_available" {
		return ErrInputInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, id)
		if e != nil {
			return e
		}
		if terminal(job.Status) {
			return ErrJobTerminal
		}
		record, e := auditRun(tx, auditID)
		if e != nil {
			return e
		}
		input, e := jobInput(tx, id)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(record.Input, input) {
			return ErrInputInvalid
		}
		if job.ResultRef != "" {
			if job.ResultRef == auditID {
				return nil
			}
			return ErrConflict
		}
		if e = tx.Exec("UPDATE e0_jobs SET result_ref=? WHERE id=?", auditID, id).Error; e != nil {
			return e
		}
		job.ResultRef = auditID
		copy := map[string]any{}
		for key, value := range payload {
			copy[key] = value
		}
		copy["result_ref"] = auditID
		return appendEvent(tx, &job, typ, copy)
	})
}
func (s *Store) SaveJobAudit(ctx context.Context, id string, result domain.Result) (record AuditRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, id)
		if e != nil {
			return e
		}
		if terminal(job.Status) {
			return ErrJobTerminal
		}
		in, e := jobInput(tx, id)
		if e != nil {
			return e
		}
		plan, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		if job.ResultRef != "" {
			record, e = auditRun(tx, job.ResultRef)
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(record.Input, in) || !reflect.DeepEqual(record.Result, result) {
				return ErrConflict
			}
			return nil
		}
		record, e = saveAuditInput(tx, plan.SnapshotID, in, result)
		if e != nil {
			return e
		}
		if e = tx.Exec("UPDATE e0_jobs SET result_ref=? WHERE id=?", record.ID, id).Error; e != nil {
			return e
		}
		job.ResultRef = record.ID
		return appendEvent(tx, &job, "findings_available", map[string]any{"phase": "L2", "result_ref": record.ID, "finding_count": len(record.Findings), "is_final": false})
	})
	return
}
func (s *Store) ReadJobAudit(ctx context.Context, id string) (job Job, record *AuditRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		job, e = get(tx, id)
		if e != nil {
			return e
		}
		if job.ResultRef != "" {
			audit, e := auditRun(tx, job.ResultRef)
			if e != nil {
				return e
			}
			record = &audit
		}
		return nil
	})
	return
}
func (s *Store) AppendJobMessage(ctx context.Context, id string, input MessageInput) (message ConversationMessage, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, id)
		if e != nil {
			return e
		}
		if job.Status != "running" {
			return ErrJobTerminal
		}
		plan, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		if input.PolicyScopeRef != plan.PolicyScopeRef || input.Role == "user" {
			return ErrInputInvalid
		}
		input.JobID = id
		var replay bool
		message, replay, e = insertJobConversationMessage(tx, job.ID, input)
		if e != nil {
			return e
		}
		if replay {
			return nil
		}
		if e = tx.Exec("UPDATE conversation_turns SET reply_message_id=? WHERE job_id=?", message.ID, id).Error; e != nil {
			return e
		}
		return appendEvent(tx, &job, "message_available", map[string]any{"message_ref": message.ID, "complete_message": true})
	})
	return
}
func (s *Store) AttachReportAndEvent(ctx context.Context, id, reportID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, id)
		if e != nil {
			return e
		}
		if job.Status != "running" {
			return ErrJobTerminal
		}
		report, e := reportRecord(tx, reportID)
		if e != nil {
			return e
		}
		if job.ResultRef == "" || report.AuditID != job.ResultRef {
			return ErrInputInvalid
		}
		return appendEvent(tx, &job, "report_available", map[string]any{"phase": "L4", "report_ref": reportID})
	})
}

type FinalizeJobReportRequest struct {
	Format        string
	Heads         []ReviewHead
	Content       []byte
	Key           string
	Status        string
	Code          string
	AgentResultID string
}

// FinalizeJobWithReport publishes a fixed report, an application message and the
// terminal event in one short transaction. A cancellation that committed first
// closes this gate; no report or normal reply is saved after that cancellation.
func (s *Store) FinalizeJobWithReport(ctx context.Context, id string, request FinalizeJobReportRequest) (record ReportRecord, err error) {
	if !enum(request.Status, "completed", "partial") || !validErrorCode(request.Code) || (request.Status == "completed" && request.Code != "") || (request.Status == "partial" && request.Code == "") {
		return record, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, id)
		if e != nil {
			return e
		}
		if job.Status != "running" {
			return ErrJobTerminal
		}
		if job.Kind != "audit" || job.ResultRef == "" {
			return ErrInputInvalid
		}
		plan, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		record, e = saveReport(tx, job.ResultRef, request.Format, request.Heads, request.Content, request.Key)
		if e != nil {
			return e
		}
		if e = appendEvent(tx, &job, "report_available", map[string]any{"phase": "L4", "report_ref": record.ID}); e != nil {
			return e
		}
		text := "已保存确定性规则结果与固定报告；检查结论及覆盖范围以固定输入和证据为准，配置允许不证明实际可达。"
		role, origin := "event", "application"
		if request.AgentResultID != "" {
			agent, e := jobAgentResult(tx, job.ID)
			if e != nil {
				return e
			}
			if agent == nil || agent.ID != request.AgentResultID || agent.Content.Output == nil || agent.Content.JobID != job.ID || plan.Mode != "rules_and_agent" {
				return ErrInputInvalid
			}
			text = agent.Content.Output.Explanation
			role, origin = "assistant", "model"
		}
		if request.Status == "partial" && request.AgentResultID == "" {
			text = "本次任务部分完成；已保存确定性规则结果与固定报告，请核对未完成阶段。"
			if request.Code == "MODEL_NOT_CONFIGURED" {
				text = "已保存确定性规则结果与固定报告；模型未配置或未获授权，模型阶段未执行，本次任务部分完成。"
			}
		}
		message, replay, e := insertJobConversationMessage(tx, job.ID, MessageInput{ClientMessageID: "workflow_result_" + job.ID, Role: role, Origin: origin, PublicText: text, PolicyScopeRef: plan.PolicyScopeRef, JobID: job.ID, RefKind: "report", RefID: record.ID})
		if e != nil {
			return e
		}
		if replay {
			return ErrConflict
		}
		if e = tx.Exec("UPDATE conversation_turns SET reply_message_id=? WHERE job_id=?", message.ID, job.ID).Error; e != nil {
			return e
		}
		if e = appendEvent(tx, &job, "message_available", map[string]any{"message_ref": message.ID, "complete_message": true}); e != nil {
			return e
		}
		return finishJob(tx, &job, request.Status, request.Code)
	})
	if err != nil {
		record = ReportRecord{}
	}
	return
}

func expireQueued(tx *gorm.DB) error {
	var jobs []Job
	if err := tx.Raw("SELECT * FROM e0_jobs WHERE status='queued' ORDER BY created_at,id LIMIT 10").Scan(&jobs).Error; err != nil {
		return err
	}
	at := time.Now().UTC()
	for _, job := range jobs {
		created, e := time.Parse(time.RFC3339Nano, job.CreatedAt)
		deadline, e2 := time.Parse(time.RFC3339Nano, job.QueueDeadline)
		root, e3 := time.Parse(time.RFC3339Nano, job.RootDeadline)
		code := ""
		if e != nil || e2 != nil || e3 != nil || at.Before(created) {
			code = "CLOCK_UNCONFIRMED"
		} else if !at.Before(deadline) {
			code = "QUEUE_DEADLINE_EXCEEDED"
		} else if !at.Before(root) {
			code = "JOB_DEADLINE_EXCEEDED"
		}
		if code != "" {
			if err := finishJob(tx, &job, "failed", code); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) ExpireQueued(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(expireQueued)
}

func (s *Store) createProbe(ctx context.Context, conversation, key string) (job Job, replayed bool, err error) {
	if !validKey(conversation) || !validKey(key) {
		return job, false, ErrInputInvalid
	}
	digest := requestDigest([]string{"e0_probe", conversation})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var found bool
		var e error
		job, found, e = existingJob(tx, key, digest)
		if e != nil {
			return e
		}
		if found {
			replayed = true
			return nil
		}
		if e = queueAdmission(tx, conversation); e != nil {
			return e
		}
		at := time.Now().UTC()
		stamp := at.Format(time.RFC3339Nano)
		if e = tx.Exec("INSERT OR IGNORE INTO conversations(id,title,policy_scope_ref,revision,archived,last_seq,created_at,updated_at) VALUES(?,'E0本机联调','',1,0,0,?,?)", conversation, stamp, stamp).Error; e != nil {
			return e
		}
		id, e := opaqueID("e0")
		if e != nil {
			return e
		}
		job = Job{ID: id, ConversationID: conversation, ClientRequestID: key, RequestDigest: digest, Kind: "e0_probe", Status: "queued", CreatedAt: stamp, QueueDeadline: at.Add(120 * time.Second).Format(time.RFC3339Nano), RootDeadline: at.Add(150 * time.Second).Format(time.RFC3339Nano), BudgetRef: "application-default"}
		if e = tx.Exec("INSERT INTO e0_jobs(id,conversation_id,client_request_id,request_digest,kind,status,created_at,queue_deadline,root_deadline,budget_ref) VALUES(?,?,?,?,?,'queued',?,?,?,?)", id, conversation, key, digest, job.Kind, stamp, job.QueueDeadline, job.RootDeadline, job.BudgetRef).Error; e != nil {
			return e
		}
		return appendEvent(tx, &job, "job_status", map[string]any{"status": "queued", "source": "synthetic"})
	})
	return
}

func validErrorCode(code string) bool {
	if len(code) > 80 {
		return false
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func finishJob(tx *gorm.DB, job *Job, status, code string) error {
	if terminal(job.Status) {
		return nil
	}
	if !enum(status, "completed", "partial", "failed", "interrupted") || !validErrorCode(code) {
		return ErrInputInvalid
	}
	if job.Status == "queued" && job.Kind != "e0_probe" && status != "failed" && status != "interrupted" {
		return ErrInputInvalid
	}
	if job.Status == "cancelling" && (status != "interrupted" || job.Kind == "e0_probe") {
		status = "cancelled"
		code = "CANCELLED"
	}
	if status == "completed" && job.Kind == "audit" && job.ResultRef == "" {
		status = "failed"
		code = "RESULT_NOT_SAVED"
	}
	if code == "" && status == "failed" {
		code = "EXECUTION_FAILED"
	}
	if code == "" && status == "partial" {
		code = "PARTIAL_RESULT"
	}
	job.Status = status
	job.ErrorCode = code
	job.FinishedAt = now()
	if err := tx.Exec("UPDATE e0_jobs SET status=?,error_code=?,finished_at=? WHERE id=? AND status IN ('queued','running','cancelling')", status, code, job.FinishedAt, job.ID).Error; err != nil {
		return err
	}
	if job.Kind != "e0_probe" {
		var turn ConversationTurn
		r := tx.Raw("SELECT * FROM conversation_turns WHERE job_id=?", job.ID).Scan(&turn)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrInputInvalid
		}
		if turn.ReplyMessageID == "" {
			plan, err := planRecord(tx, job.PlanID)
			if err != nil {
				return err
			}
			text := map[string]string{"completed": "本次任务流程已结束；检查结论和覆盖范围以保存结果为准。", "partial": "本次任务部分完成；已保存结果仍可查看，请核对未完成阶段。", "failed": "本次任务未完成；已保存结果保留，请查看受控错误摘要。", "cancelled": "本次任务已取消；已保存结果保留。", "interrupted": "本次任务已中断；已保存结果保留，未自动重放调用。"}[status]
			message, _, err := insertJobConversationMessage(tx, job.ID, MessageInput{ClientMessageID: "terminal_" + job.ID, Role: "event", Origin: "application", PublicText: text, PolicyScopeRef: plan.PolicyScopeRef, JobID: job.ID, RefKind: "job", RefID: job.ID})
			if err != nil {
				return err
			}
			if err = tx.Exec("UPDATE conversation_turns SET reply_message_id=? WHERE job_id=?", message.ID, job.ID).Error; err != nil {
				return err
			}
			if err = appendEvent(tx, job, "message_available", map[string]any{"message_ref": message.ID, "complete_message": true}); err != nil {
				return err
			}
		}
		if err := tx.Exec("UPDATE conversation_turns SET state=? WHERE job_id=?", status, job.ID).Error; err != nil {
			return err
		}
	}
	return appendEvent(tx, job, "job_terminal", map[string]any{"status": status, "error_code": code})
}

func (s *Store) FinishWithCode(ctx context.Context, id, status, code string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := get(tx, id)
		if err != nil {
			return err
		}
		return finishJob(tx, &job, status, code)
	})
}

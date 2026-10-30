package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

type BudgetError string

func (e BudgetError) Error() string     { return string(e) }
func (e BudgetError) ErrorCode() string { return string(e) }

const (
	ErrBudgetExhausted     BudgetError = "ATTEMPT_LIMIT"
	ErrBudgetClosed        BudgetError = "EXECUTION_CLOSED"
	ErrBudgetNotConfigured BudgetError = "BUDGET_NOT_CONFIGURED"
	ErrBudgetExpired       BudgetError = "BUDGET_EXPIRED"
	ErrAttemptEffectDenied BudgetError = "EFFECT_DENIED"
	ErrAttemptConfigStale  BudgetError = "CONFIGURATION_STALE"
)

// BudgetPolicy is an explicitly configured authorization period. A new task,
// conversation, key, process or source alias never creates a new period.
// Limits count physical attempts, including failures and unknown delivery; they
// are not currency limits or statements about provider billing.
type BudgetPolicy struct {
	Ref                      string `json:"ref"`
	ApplicationCloudAttempts int64  `json:"application_cloud_attempts"`
	ApplicationModelAttempts int64  `json:"application_model_attempts"`
	SessionCloudAttempts     int64  `json:"session_cloud_attempts"`
	SessionModelAttempts     int64  `json:"session_model_attempts"`
	ExpiresAt                string `json:"expires_at"`
	CreatedAt                string `json:"created_at"`
}

type OutboundAttempt struct {
	ID             string             `json:"id"`
	JobID          string             `json:"job_id"`
	ConversationID string             `json:"conversation_id"`
	BudgetRef      string             `json:"budget_ref"`
	Class          string             `json:"class"`
	Bucket         string             `json:"bucket"`
	SourceRef      string             `json:"source_ref"`
	State          string             `json:"state"`
	ErrorCode      string             `json:"error_code,omitempty"`
	HTTPStatus     int                `json:"http_status,omitempty"`
	DurationMS     int64              `json:"duration_ms"`
	ReservedAt     string             `json:"reserved_at"`
	FinishedAt     string             `json:"finished_at,omitempty"`
	Usage          *domain.ModelUsage `json:"usage,omitempty" gorm:"-"`
	UsageJSON      *string            `json:"-"`
}

type AttemptUsage struct {
	CloudAttempts        int64              `json:"cloud_attempts"`
	ModelAttempts        int64              `json:"model_attempts"`
	UnknownDelivery      int64              `json:"unknown_delivery"`
	KnownUsageResponses  int64              `json:"known_usage_responses"`
	UnknownUsageAttempts int64              `json:"unknown_usage_attempts"`
	ReportedTokens       *domain.ModelUsage `json:"reported_tokens" gorm:"-"`
	UsageTotalsOverflow  bool               `json:"usage_totals_overflow"`
}

// migrateBudgets is called only by an explicit schema migration. Opening or
// constructing a client never creates a budget or changes an authorization.
func migrateBudgets(tx *gorm.DB) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS budget_policies(ref TEXT PRIMARY KEY,application_cloud_attempts INTEGER NOT NULL CHECK(application_cloud_attempts>=0),application_model_attempts INTEGER NOT NULL CHECK(application_model_attempts>=0),session_cloud_attempts INTEGER NOT NULL CHECK(session_cloud_attempts>=0),session_model_attempts INTEGER NOT NULL CHECK(session_model_attempts>=0),expires_at TEXT NOT NULL,created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS outbound_attempts(id TEXT PRIMARY KEY,job_id TEXT NOT NULL REFERENCES e0_jobs(id),conversation_id TEXT NOT NULL REFERENCES conversations(id),budget_ref TEXT NOT NULL REFERENCES budget_policies(ref),class TEXT NOT NULL CHECK(class IN ('cloud','credential','model')),bucket TEXT NOT NULL CHECK(bucket IN ('cloud','model')),source_ref TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('reserved','response','unknown','failed')),error_code TEXT NOT NULL DEFAULT '',http_status INTEGER NOT NULL DEFAULT 0,duration_ms INTEGER NOT NULL DEFAULT 0 CHECK(duration_ms>=0),reserved_at TEXT NOT NULL,finished_at TEXT NOT NULL DEFAULT '',usage_json TEXT CHECK(usage_json IS NULL OR json_valid(usage_json)))`,
		`CREATE INDEX IF NOT EXISTS outbound_application_usage ON outbound_attempts(budget_ref,bucket)`,
		`CREATE INDEX IF NOT EXISTS outbound_session_usage ON outbound_attempts(budget_ref,conversation_id,bucket)`,
		`CREATE INDEX IF NOT EXISTS outbound_job_usage ON outbound_attempts(job_id,bucket)`,
	} {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	// Explicit migration also repairs a schema 3 ledger created before this
	// nullable projection, preserving unknown rows instead of inventing usage.
	var usageColumn int64
	if err := tx.Raw("SELECT count(*) FROM pragma_table_info('outbound_attempts') WHERE name='usage_json'").Scan(&usageColumn).Error; err != nil {
		return err
	}
	if usageColumn == 0 {
		return tx.Exec("ALTER TABLE outbound_attempts ADD COLUMN usage_json TEXT CHECK(usage_json IS NULL OR json_valid(usage_json))").Error
	}
	return nil
}

func boundedBudgetLimit(value int64) bool { return value >= 0 && value <= 1_000_000_000 }

func (s *Store) ConfigureBudgetPolicy(ctx context.Context, policy BudgetPolicy) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return configureBudgetPolicy(tx, policy) })
}

func budgetPolicy(tx *gorm.DB, ref string) (policy BudgetPolicy, err error) {
	r := tx.Raw("SELECT * FROM budget_policies WHERE ref=?", ref).Scan(&policy)
	if r.Error != nil {
		return policy, r.Error
	}
	if r.RowsAffected != 1 {
		return policy, ErrBudgetNotConfigured
	}
	return policy, nil
}
func (s *Store) GetBudgetPolicy(ctx context.Context, ref string) (BudgetPolicy, error) {
	return budgetPolicy(s.db.WithContext(ctx), ref)
}

func attemptAlias(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ReserveAttempt must commit before the transport sends bytes. The accepted
// job/plan is the authority; callers cannot supply a fresh budget reference or
// effects. Credentials and cloud APIs share one cloud bucket at every level.
func (s *Store) ReserveAttempt(ctx context.Context, jobID, class, sourceRef, configRevision string) (string, error) {
	return s.reserveAttempt(ctx, jobID, class, sourceRef, configRevision, nil)
}
func (s *Store) reserveAttempt(ctx context.Context, jobID, class, sourceRef, configRevision string, private *AttemptCeiling) (id string, err error) {
	if !enum(class, "model", "cloud", "credential") || !attemptAlias(sourceRef) {
		return "", ErrInputInvalid
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, jobID)
		if e != nil {
			return e
		}
		at := time.Now().UTC()
		started, e1 := time.Parse(time.RFC3339Nano, job.StartedAt)
		execution, e2 := time.Parse(time.RFC3339Nano, job.ExecutionDeadline)
		root, e3 := time.Parse(time.RFC3339Nano, job.RootDeadline)
		if job.Status != "running" || e1 != nil || e2 != nil || e3 != nil || at.Before(started) || !at.Before(execution) || !at.Before(root) {
			return ErrBudgetClosed
		}
		plan, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		if configRevision == "" || plan.ConfigRevision != configRevision {
			return ErrAttemptConfigStale
		}
		if plan.BudgetRef != job.BudgetRef {
			return ErrAttemptEffectDenied
		}
		authority, e := batchAuthority(tx, job)
		if e != nil {
			return e
		}
		authorityPlan := plan
		taskIDs := []string{job.ID}
		if authority.ID != job.ID || plan.Batch != nil {
			if authority.Status != "running" {
				return ErrBudgetClosed
			}
			deadline, e := earlierBatchDeadline(authority)
			if e != nil || !at.Before(deadline) {
				return ErrBudgetClosed
			}
			authorityPlan, e = planRecord(tx, authority.PlanID)
			if e != nil {
				return e
			}
			taskIDs, e = batchAttemptJobIDs(tx, authority.ID)
			if e != nil {
				return e
			}
		}
		bucket, taskLimit := "cloud", int64(plan.EffectCeiling.CloudHTTPAttempts)
		if class == "model" {
			// The current single configured model has a server-owned alias. Input
			// DataSourceID is deliberately independent from model configuration.
			if sourceRef != "configured-model" {
				return ErrAttemptEffectDenied
			}
			bucket, taskLimit = "model", int64(plan.EffectCeiling.ModelHTTPAttempts)
			if !plan.EffectCeiling.AllowModelRequests || !(job.Kind == "assistant_turn" || job.Kind == "audit" && plan.Mode == "rules_and_agent") {
				return ErrAttemptEffectDenied
			}
		} else if !plan.EffectCeiling.AllowCloudReads || !enum(job.Kind, "audit", "collect", "connection_check") {
			return ErrAttemptEffectDenied
		}
		rootLimit := int64(authorityPlan.EffectCeiling.CloudHTTPAttempts)
		if class == "model" {
			rootLimit = int64(authorityPlan.EffectCeiling.ModelHTTPAttempts)
			if !authorityPlan.EffectCeiling.AllowModelRequests {
				return ErrAttemptEffectDenied
			}
		} else if !authorityPlan.EffectCeiling.AllowCloudReads {
			return ErrAttemptEffectDenied
		}
		policy, e := budgetPolicy(tx, job.BudgetRef)
		if e != nil {
			return e
		}
		head, e := budgetHead(tx, job.BudgetRef)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(head.Policy, policy) {
			return ErrBudgetClosed
		}
		if policy.ExpiresAt != "" {
			expires, e := time.Parse(time.RFC3339Nano, policy.ExpiresAt)
			if e != nil || !at.Before(expires) {
				return ErrBudgetExpired
			}
		}
		appLimit, sessionLimit := policy.ApplicationCloudAttempts, policy.SessionCloudAttempts
		if bucket == "model" {
			appLimit, sessionLimit = policy.ApplicationModelAttempts, policy.SessionModelAttempts
		}
		if private != nil {
			if private.Ref != "" && private.Ref != job.BudgetRef {
				return ErrAttemptEffectDenied
			}
			if private.ExpiresAt != "" {
				expiry, e := time.Parse(time.RFC3339Nano, private.ExpiresAt)
				if e != nil || !at.Before(expiry) {
					return ErrBudgetExpired
				}
			}
			if private.ApplicationAttempts != nil {
				appLimit = min(appLimit, *private.ApplicationAttempts)
			}
			if private.SessionAttempts != nil {
				sessionLimit = min(sessionLimit, *private.SessionAttempts)
			}
		}
		if taskLimit <= 0 || appLimit <= 0 || sessionLimit <= 0 {
			return ErrBudgetExhausted
		}
		var used struct{ Application, Session, Task int64 }
		e = tx.Raw(`SELECT count(*) AS application,coalesce(sum(CASE WHEN conversation_id=? THEN 1 ELSE 0 END),0) AS session,coalesce(sum(CASE WHEN job_id IN ? THEN 1 ELSE 0 END),0) AS task FROM outbound_attempts WHERE budget_ref=? AND bucket=?`, job.ConversationID, taskIDs, job.BudgetRef, bucket).Scan(&used).Error
		if e != nil {
			return e
		}
		if used.Application >= appLimit || used.Session >= sessionLimit || used.Task >= rootLimit {
			return ErrBudgetExhausted
		}
		if authority.ID != job.ID {
			var own int64
			if e = tx.Raw("SELECT count(*) FROM outbound_attempts WHERE job_id=? AND bucket=?", job.ID, bucket).Scan(&own).Error; e != nil {
				return e
			}
			if own >= taskLimit {
				return ErrBudgetExhausted
			}
		} else if used.Task >= taskLimit {
			return ErrBudgetExhausted
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		id, e = opaqueID("attempt")
		if e != nil {
			return e
		}
		return tx.Exec("INSERT INTO outbound_attempts(id,job_id,conversation_id,budget_ref,class,bucket,source_ref,state,reserved_at,usage_json) VALUES(?,?,?,?,?,?,?,'reserved',?,NULL)", id, job.ID, job.ConversationID, job.BudgetRef, class, bucket, sourceRef, at.Format(time.RFC3339Nano)).Error
	})
	if err != nil {
		id = ""
	}
	return
}

func (s *Store) RecordAttemptResult(ctx context.Context, id, state, code string, httpStatus int, durationMS int64) error {
	return s.RecordAttemptResultWithUsage(ctx, id, state, code, httpStatus, durationMS, nil)
}

func (s *Store) RecordAttemptResultWithUsage(ctx context.Context, id, state, code string, httpStatus int, durationMS int64, usage *domain.ModelUsage) error {
	if usage != nil {
		copy := *usage
		usage = &copy
	}
	controlled := enum(code, "", "UPSTREAM_TRANSPORT", "UPSTREAM_HTTP_ERROR", "REQUEST_CANCELLED", "REQUEST_DEADLINE_EXCEEDED", "REDIRECT_REFUSED", "UPSTREAM_ENCODING_UNSUPPORTED", "UPSTREAM_READ", "UPSTREAM_RESPONSE_TOO_LARGE", "UPSTREAM_PROTOCOL")
	if !enum(state, "response", "unknown", "failed") || !controlled || durationMS < 0 || durationMS > 86_400_000 || httpStatus < 0 || httpStatus > 599 || state == "response" && httpStatus < 100 || state != "response" && httpStatus != 0 {
		return ErrInputInvalid
	}
	if usage != nil && (!usage.Valid() || state != "response" || code != "" || httpStatus < 200 || httpStatus >= 300) {
		return ErrInputInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return recordAttemptResult(tx, id, state, code, httpStatus, durationMS, usage)
	})
}

func decodeAttemptUsage(attempt *OutboundAttempt) error {
	if attempt.UsageJSON == nil {
		return nil
	}
	var wire struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		TotalTokens      *int64 `json:"total_tokens"`
	}
	if json.Unmarshal([]byte(*attempt.UsageJSON), &wire) != nil || wire.PromptTokens == nil || wire.CompletionTokens == nil || wire.TotalTokens == nil {
		return ErrInputInvalid
	}
	usage := domain.ModelUsage{PromptTokens: *wire.PromptTokens, CompletionTokens: *wire.CompletionTokens, TotalTokens: *wire.TotalTokens}
	if !usage.Valid() || attempt.Class != "model" || attempt.State != "response" || attempt.ErrorCode != "" || attempt.HTTPStatus < 200 || attempt.HTTPStatus >= 300 {
		return ErrInputInvalid
	}
	attempt.Usage = &usage
	return nil
}

func recordAttemptResult(tx *gorm.DB, id, state, code string, httpStatus int, durationMS int64, usage *domain.ModelUsage) error {
	var attempt OutboundAttempt
	r := tx.Raw("SELECT * FROM outbound_attempts WHERE id=?", id).Scan(&attempt)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrNotFound
	}
	if usage != nil && attempt.Class != "model" {
		return ErrInputInvalid
	}
	if err := decodeAttemptUsage(&attempt); err != nil {
		return err
	}
	if attempt.State != "reserved" {
		if attempt.State == state && attempt.ErrorCode == code && attempt.HTTPStatus == httpStatus && attempt.DurationMS == durationMS && reflect.DeepEqual(attempt.Usage, usage) {
			return nil
		}
		return ErrConflict
	}
	var encoded any
	if usage != nil {
		body, err := marshalBounded(*usage, 256)
		if err != nil {
			return err
		}
		encoded = string(body)
	}
	return tx.Exec("UPDATE outbound_attempts SET state=?,error_code=?,http_status=?,duration_ms=?,finished_at=?,usage_json=? WHERE id=? AND state='reserved'", state, code, httpStatus, durationMS, now(), encoded, id).Error
}

func (s *Store) BudgetUsage(ctx context.Context, budgetRef, conversationID, jobID string) (usage AttemptUsage, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		usage, e = budgetUsage(tx, budgetRef, conversationID, jobID)
		return e
	})
	return
}

func budgetUsage(tx *gorm.DB, budgetRef, conversationID, jobID string) (usage AttemptUsage, err error) {
	where := ` FROM outbound_attempts WHERE budget_ref=?`
	args := []any{budgetRef}
	if conversationID != "" {
		where += " AND conversation_id=?"
		args = append(args, conversationID)
	}
	if jobID != "" {
		where += " AND (job_id=? OR job_id IN (SELECT id FROM e0_jobs WHERE parent_job_id=?))"
		args = append(args, jobID, jobID)
	}
	err = func() error {
		query := `SELECT coalesce(sum(CASE WHEN bucket='cloud' THEN 1 ELSE 0 END),0) AS cloud_attempts,coalesce(sum(CASE WHEN bucket='model' THEN 1 ELSE 0 END),0) AS model_attempts,coalesce(sum(CASE WHEN state IN ('reserved','unknown') THEN 1 ELSE 0 END),0) AS unknown_delivery` + where
		if err := tx.Raw(query, args...).Scan(&usage).Error; err != nil {
			return err
		}
		rows, err := tx.Raw("SELECT class,state,error_code,http_status,usage_json"+where+" AND bucket='model' AND usage_json IS NOT NULL", args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		total := domain.ModelUsage{}
		for rows.Next() {
			var attempt OutboundAttempt
			if err = rows.Scan(&attempt.Class, &attempt.State, &attempt.ErrorCode, &attempt.HTTPStatus, &attempt.UsageJSON); err != nil {
				return err
			}
			if err = decodeAttemptUsage(&attempt); err != nil {
				return err
			}
			usage.KnownUsageResponses++
			value := attempt.Usage
			if value.PromptTokens > domain.MaxReportedModelTokens-total.PromptTokens || value.CompletionTokens > domain.MaxReportedModelTokens-total.CompletionTokens || value.TotalTokens > domain.MaxReportedModelTokens-total.TotalTokens {
				usage.UsageTotalsOverflow = true
			}
			if !usage.UsageTotalsOverflow {
				total.PromptTokens += value.PromptTokens
				total.CompletionTokens += value.CompletionTokens
				total.TotalTokens += value.TotalTokens
			}
		}
		if err = rows.Err(); err != nil {
			return err
		}
		usage.UnknownUsageAttempts = usage.ModelAttempts - usage.KnownUsageResponses
		if usage.KnownUsageResponses > 0 && !usage.UsageTotalsOverflow {
			usage.ReportedTokens = &total
		}
		return nil
	}()
	return
}

func (s *Store) JobAttempts(ctx context.Context, jobID string) (items []OutboundAttempt, err error) {
	items = []OutboundAttempt{}
	err = s.db.WithContext(ctx).Raw("SELECT * FROM outbound_attempts WHERE job_id=? ORDER BY reserved_at,id LIMIT 106", jobID).Scan(&items).Error
	if err == nil {
		for n := range items {
			if err = decodeAttemptUsage(&items[n]); err != nil {
				break
			}
		}
	}
	return
}

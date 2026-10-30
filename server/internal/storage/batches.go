package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/workload"
	"gorm.io/gorm"
)

// BatchSpec is an explicitly accepted, finite set in one policy partition.
// The plan owns its source, configuration, mode, data policy and total limits.
// Size is a segment ceiling, never a truncation of Resources.
type BatchSpec struct {
	Resources []PreparedResource `json:"resources"`
	Size      int                `json:"size"`
}

type BatchPhaseUsage struct {
	CollectionMillis int64 `json:"collection_ms"`
	RulesMillis      int64 `json:"rules_ms"`
	ModelMillis      int64 `json:"model_ms"`
	FinalizeMillis   int64 `json:"finalize_ms"`
}
type BatchUsage struct {
	UsageUnknown    bool            `json:"usage_unknown"`
	UnknownReasons  []string        `json:"unknown_reasons"`
	CloudAttempts   int64           `json:"cloud_attempts"`
	ModelAttempts   int64           `json:"model_attempts"`
	ModelCalls      int64           `json:"recorded_model_calls"`
	ToolCalls       int64           `json:"recorded_tool_calls"`
	ExecutionMillis int64           `json:"execution_ms"`
	Phases          BatchPhaseUsage `json:"phases"`
}
type BatchSegment struct {
	Ordinal         int                   `json:"ordinal"`
	Resources       []PreparedResource    `json:"resources"`
	ChildJobID      string                `json:"child_job_id,omitempty"`
	Status          string                `json:"status"`
	ErrorCode       string                `json:"error_code,omitempty"`
	StartedAt       string                `json:"started_at,omitempty"`
	FinishedAt      string                `json:"finished_at,omitempty"`
	SnapshotID      string                `json:"snapshot_id,omitempty"`
	AuditRef        string                `json:"audit_ref,omitempty"`
	ReportRef       string                `json:"report_ref,omitempty"`
	Manifest        *domain.InputManifest `json:"input_manifest,omitempty"`
	Phases          BatchPhaseUsage       `json:"phases"`
	ExecutionMillis int64                 `json:"execution_ms"`
	PhaseSource     string                `json:"phase_source,omitempty"`
}
type BatchRequest struct {
	ParentJobID    string             `json:"parent_job_id"`
	PlanID         string             `json:"plan_id"`
	SourceRef      string             `json:"source_ref"`
	ConfigRevision string             `json:"-"`
	Scope          domain.Scope       `json:"scope"`
	Spec           BatchSpec          `json:"spec"`
	Workload       workload.Resolved  `json:"workload"`
	Effects        EffectCeiling      `json:"effects"`
	AcceptedAt     string             `json:"accepted_at"`
	Status         string             `json:"status"`
	ErrorCode      string             `json:"error_code,omitempty"`
	Segments       []BatchSegment     `json:"segments"`
	Covered        []PreparedResource `json:"covered"`
	Failed         []PreparedResource `json:"failed"`
	Unprocessed    []PreparedResource `json:"unprocessed"`
	Usage          BatchUsage         `json:"usage"`
}
type batchRow struct {
	ParentJobID, PlanID, AcceptedAt, RequestDigest, Body, HeadsJSON, ScopeKey, State, ErrorCode, FinishedAt string
	ProfileWatermark, IntentWatermark                                                                       int64
}
type batchSegmentRow struct {
	ParentJobID                                                                                                                                string
	Ordinal                                                                                                                                    int
	ResourcesJSON, ChildJobID, Status, ErrorCode, StartedAt, FinishedAt, SnapshotID, AuditRef, ReportRef, ManifestJSON, PhaseJSON, PhaseSource string
	ExecutionMillis                                                                                                                            int64
}

// Called only by explicit migration. Internal rows occupy neither a queue entry
// nor a second conversation slot; their parent is the sole worker owner.
func migrateBatchSchema(tx *gorm.DB) error {
	var n int64
	if e := tx.Raw("SELECT count(*) FROM pragma_table_info('e0_jobs') WHERE name='parent_job_id'").Scan(&n).Error; e != nil {
		return e
	}
	if n == 0 {
		if e := tx.Exec("ALTER TABLE e0_jobs ADD COLUMN parent_job_id TEXT REFERENCES e0_jobs(id)").Error; e != nil {
			return e
		}
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS batch_requests(parent_job_id TEXT PRIMARY KEY REFERENCES e0_jobs(id),plan_id TEXT UNIQUE NOT NULL REFERENCES execution_plans(id),accepted_at TEXT NOT NULL,request_digest TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),profile_watermark INTEGER NOT NULL,intent_watermark INTEGER NOT NULL,heads_json TEXT NOT NULL CHECK(json_valid(heads_json)),scope_key TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('accepted','running','completed','partial','failed','cancelled','interrupted')),error_code TEXT NOT NULL DEFAULT '',finished_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS batch_segments(parent_job_id TEXT NOT NULL REFERENCES batch_requests(parent_job_id),ordinal INTEGER NOT NULL CHECK(ordinal>0),resources_json TEXT NOT NULL CHECK(json_valid(resources_json)),child_job_id TEXT UNIQUE REFERENCES e0_jobs(id),status TEXT NOT NULL CHECK(status IN ('pending','running','completed','partial','failed','cancelled','interrupted','unprocessed')),error_code TEXT NOT NULL DEFAULT '',started_at TEXT NOT NULL DEFAULT '',finished_at TEXT NOT NULL DEFAULT '',snapshot_id TEXT NOT NULL DEFAULT '',audit_ref TEXT NOT NULL DEFAULT '',report_ref TEXT NOT NULL DEFAULT '',manifest_json TEXT NOT NULL DEFAULT '',phase_json TEXT NOT NULL DEFAULT '{}',phase_source TEXT NOT NULL DEFAULT '',execution_millis INTEGER NOT NULL DEFAULT 0 CHECK(execution_millis>=0),PRIMARY KEY(parent_job_id,ordinal))`,
		`DROP INDEX IF EXISTS e0_one_active`,
		`CREATE UNIQUE INDEX e0_one_active ON e0_jobs(conversation_id) WHERE parent_job_id IS NULL AND status IN ('queued','running','cancelling')`,
		`CREATE UNIQUE INDEX IF NOT EXISTS e0_one_active_batch_segment ON e0_jobs(parent_job_id) WHERE parent_job_id IS NOT NULL AND status IN ('running','cancelling')`,
		`CREATE INDEX IF NOT EXISTS batch_child_parent ON e0_jobs(parent_job_id,id)`,
	} {
		if e := tx.Exec(q).Error; e != nil {
			return e
		}
	}
	if e := migrateBatchTurns(tx); e != nil {
		return e
	}
	return checkBatchSchema(tx)
}
func checkBatchSchema(tx *gorm.DB) error {
	for table, cols := range map[string][]string{"e0_jobs": {"parent_job_id"}, "batch_requests": {"parent_job_id", "plan_id", "accepted_at", "request_digest", "body", "profile_watermark", "intent_watermark", "heads_json", "scope_key", "state", "error_code", "finished_at"}, "batch_segments": {"parent_job_id", "ordinal", "resources_json", "child_job_id", "status", "error_code", "started_at", "finished_at", "snapshot_id", "audit_ref", "report_ref", "manifest_json", "phase_json", "phase_source", "execution_millis"}} {
		var names []struct{ Name string }
		if e := tx.Raw("SELECT name FROM pragma_table_info(?)", table).Scan(&names).Error; e != nil {
			return e
		}
		found := map[string]bool{}
		for _, v := range names {
			found[v.Name] = true
		}
		for _, col := range cols {
			if !found[col] {
				return errors.New("batch schema incomplete; explicit migration required")
			}
		}
	}
	for name, marker := range map[string]string{"e0_one_active": "parent_job_id is null", "e0_one_active_batch_segment": "parent_job_id is not null"} {
		var sqlText string
		r := tx.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&sqlText)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 || !strings.Contains(strings.Join(strings.Fields(strings.ToLower(sqlText)), " "), marker) {
			return errors.New("batch active-slot indexes incomplete; explicit migration required")
		}
	}
	unique, e := batchMessageUnique(tx)
	if e != nil {
		return e
	}
	if unique {
		return errors.New("batch turn relation incomplete; explicit migration required")
	}
	return nil
}
func batchSpec(p ExecutionPlan) (BatchSpec, error) {
	pinned := p.Preparation == nil && p.DataSourceID == "local-offline"
	scopeValid := validConfiguredScope(p.PreparedInput.Scope)
	if pinned {
		scopeValid = p.PreparedInput.Scope.Ref != ""
	} // Exact snapshot/domain validation follows below.
	if p.Batch == nil || p.Kind != "audit" || p.ContextReportID != "" || p.RecheckOfJobID != "" || !scopeValid || p.PreparedInput.Scope.Boundary != "internet" || p.PolicyScopeRef != p.PreparedInput.Scope.Ref || p.Batch.Size < 1 || p.Batch.Size > p.Workload.Limits.AssetsPerBatch || len(p.Batch.Resources) <= p.Batch.Size {
		return BatchSpec{}, ErrInputInvalid
	}
	encoded, e := marshalBounded(p.Batch, 1<<20)
	if e != nil {
		return BatchSpec{}, e
	}
	var out BatchSpec
	if json.Unmarshal(encoded, &out) != nil {
		return out, ErrInputInvalid
	}
	seen, assets := map[string]bool{}, map[string]bool{}
	for i := range out.Resources {
		r := &out.Resources[i]
		if p.Preparation != nil && r.AssetID == "" {
			r.AssetID = r.ResourceID
		}
		identityValid := attemptAlias
		if pinned {
			identityValid = validKey
		}
		if !identityValid(r.AssetID) || assets[r.AssetID] || r.ResourceType != "ecs" || r.ResourceID != "" && !identityValid(r.ResourceID) || r.Region != "" && !identityValid(r.Region) {
			return out, ErrInputInvalid
		}
		assets[r.AssetID] = true
		key := r.Region + "\x00" + r.ResourceID
		if r.ResourceID == "" {
			key = "local\x00" + r.AssetID
		}
		if seen[key] {
			return out, ErrInputInvalid
		}
		seen[key] = true
	}
	return out, nil
}
func batchParts(spec BatchSpec) [][]PreparedResource {
	out := [][]PreparedResource{}
	for i := 0; i < len(spec.Resources); i += spec.Size {
		end := min(i+spec.Size, len(spec.Resources))
		out = append(out, append([]PreparedResource{}, spec.Resources[i:end]...))
	}
	return out
}

func batchChildTemplate(p ExecutionPlan, resources []PreparedResource) (ExecutionPlan, error) {
	p.ID, p.ConsumedJobID, p.CreatedAt, p.ExpiresAt, p.Body, p.PreparedJSON, p.HeadsJSON = "", "", "", "", "", "", ""
	p.Batch = nil
	selected := map[string]bool{}
	for _, r := range resources {
		selected[r.AssetID] = true
	}
	p.Heads = append([]AssetHeadVersion{}, p.Heads...)
	heads := []AssetHeadVersion{}
	for _, h := range p.Heads {
		if selected[h.AssetID] {
			heads = append(heads, h)
		}
	}
	p.Heads = heads
	if p.Preparation != nil {
		raw, _ := json.Marshal(p.Preparation)
		var spec PreparationSpec
		if json.Unmarshal(raw, &spec) != nil {
			return p, ErrInputInvalid
		}
		spec.Selected = append([]PreparedResource{}, resources...)
		spec.Query.Name, spec.Query.Group = "", ""
		spec.Query.Tags = nil
		spec.Query.ResourceID, spec.Query.Region = "", ""
		spec.Query.ResourceIDs, spec.Query.Regions = []string{}, []string{}
		for _, r := range resources {
			spec.Query.ResourceIDs = append(spec.Query.ResourceIDs, r.ResourceID)
			if !containsPrepared(spec.Query.Regions, r.Region) {
				spec.Query.Regions = append(spec.Query.Regions, r.Region)
			}
		}
		spec.Query.ResourceType = "ecs"
		spec.AllowMultiple = len(resources) > 1
		spec.ResultLimit = len(resources)
		p.Preparation = &spec
	} else {
		raw, _ := json.Marshal(p.PreparedInput)
		var in domain.Input
		if json.Unmarshal(raw, &in) != nil {
			return p, ErrInputInvalid
		}
		in.Assets = []domain.Asset{}
		in.Selection = domain.Selection{Kind: "assets", AssetIDs: []string{}}
		for _, a := range p.PreparedInput.Assets {
			if selected[a.ID] {
				in.Assets = append(in.Assets, a)
				in.Selection.AssetIDs = append(in.Selection.AssetIDs, a.ID)
			}
		}
		if len(in.Assets) != len(resources) {
			return p, ErrInputInvalid
		}
		in.Baseline.Exceptions = []domain.Exception{}
		for _, x := range p.PreparedInput.Baseline.Exceptions {
			if selected[x.SubjectRef] {
				in.Baseline.Exceptions = append(in.Baseline.Exceptions, x)
			}
		}
		p.PreparedInput = in
	}
	return p, nil
}
func validateBatchPlan(tx *gorm.DB, p ExecutionPlan) error {
	spec, e := batchSpec(p)
	if e != nil {
		return e
	}
	if p.Preparation == nil {
		if p.DataSourceID != "local-offline" || p.SnapshotID == "" || p.EffectCeiling.AllowCloudReads || p.PreparedInput.Selection.Kind != "assets" || len(p.PreparedInput.Assets) != len(spec.Resources) {
			return ErrInputInvalid
		}
		snap, e := snapshot(tx, p.SnapshotID)
		if e != nil {
			return e
		}
		if !validResolvedConfiguration(snap.Input, p.PreparedInput) {
			return ErrInputInvalid
		}
		expected, expectedHeads, e := preparedSelection(tx, p.SnapshotID, p.PreparedInput.Selection)
		if e != nil {
			return e
		}
		expected.EvaluationAt = p.PreparedInput.EvaluationAt
		if !reflect.DeepEqual(expected, p.PreparedInput) || !reflect.DeepEqual(expectedHeads, p.Heads) {
			return ErrPlanStale
		}
		byID := map[string]domain.Asset{}
		for _, a := range p.PreparedInput.Assets {
			byID[a.ID] = a
		}
		for _, r := range spec.Resources {
			a, ok := byID[r.AssetID]
			if !ok || r.ResourceID != "" && r.ResourceID != a.ProviderResourceID || r.Region != "" && r.Region != a.Region {
				return ErrInputInvalid
			}
		}
	} else {
		if p.DataSourceID == "local-offline" || p.SnapshotID != "" || p.Preparation.Query.Name != "" || p.Preparation.Query.Group != "" || len(p.Preparation.Query.Tags) != 0 {
			return ErrInputInvalid
		}
		if p.Preparation.Query.ResourceType != "ecs" || p.Preparation.Boundary != p.PreparedInput.Scope.Boundary || p.Preparation.Direction != p.PreparedInput.Scope.Direction || !p.Preparation.AllowMultiple || p.Preparation.ResultLimit != len(spec.Resources) {
			return ErrInputInvalid
		}
		expectedIDs := map[string]bool{}
		for _, r := range spec.Resources {
			expectedIDs[r.ResourceID] = true
		}
		ids := preparationIDs(p.Preparation.Query)
		if len(ids) != len(expectedIDs) {
			return ErrInputInvalid
		}
		for _, id := range ids {
			if !expectedIDs[id] {
				return ErrInputInvalid
			}
			delete(expectedIDs, id)
		}
		for _, r := range spec.Resources {
			if !strings.HasPrefix(r.ResourceID, "i-") || r.Region == "" {
				return ErrInputInvalid
			}
		}
	}
	for _, part := range batchParts(spec) {
		child, e := batchChildTemplate(p, part)
		if e != nil {
			return e
		}
		if child.Preparation != nil {
			if _, e = validatePreparationGrant(tx, child); e != nil {
				return e
			}
		} else {
			bounded, e := audit.WithInputObjectLimits(tx.Statement.Context, len(part), p.Workload.Limits.MaxPolicies, p.Workload.Limits.MaxQueryCells)
			if e != nil {
				return e
			}
			if e = audit.ValidateContext(bounded, child.PreparedInput); e != nil {
				return ErrInputInvalid
			}
		}
	}
	return nil
}

func recordBatchAcceptance(tx *gorm.DB, p ExecutionPlan, job Job) error {
	if p.Batch == nil {
		return nil
	}
	if job.ParentJobID != "" || job.PlanID != p.ID || job.Kind != p.Kind || job.ConversationID != p.ConversationID || job.BudgetRef != p.BudgetRef || !reflect.DeepEqual(job.Workload, p.Workload) {
		return ErrInputInvalid
	}
	if e := validateBatchPlan(tx, p); e != nil {
		return e
	}
	spec, e := batchSpec(p)
	if e != nil {
		return e
	}
	value := BatchRequest{ParentJobID: job.ID, PlanID: p.ID, SourceRef: p.DataSourceID, ConfigRevision: p.ConfigRevision, Scope: p.PreparedInput.Scope, Spec: spec, Workload: p.Workload, Effects: p.EffectCeiling, AcceptedAt: job.CreatedAt, Status: "accepted"}
	body, e := marshalBounded(value, 2<<20)
	if e != nil {
		return e
	}
	var profiles, intents int64
	if e = tx.Raw("SELECT COALESCE(max(rowid),0) FROM profile_versions").Scan(&profiles).Error; e != nil {
		return e
	}
	if e = tx.Raw("SELECT COALESCE(max(rowid),0) FROM intent_versions").Scan(&intents).Error; e != nil {
		return e
	}
	key := canonicalBusinessScope(p.PreparedInput.Scope)
	heads := []AssetHeadVersion{}
	for _, r := range spec.Resources {
		h, e := loadAssetHead(tx, key, r.AssetID)
		if e != nil {
			return e
		}
		heads = append(heads, h)
	}
	if p.Preparation == nil {
		accepted := map[string]AssetHeadVersion{}
		for _, h := range p.Heads {
			accepted[h.AssetID] = h
		}
		for _, h := range heads {
			if expected, ok := accepted[h.AssetID]; ok && expected != h {
				return ErrPlanStale
			}
		}
	}
	hbody, e := marshalBounded(heads, 1<<20)
	if e != nil {
		return e
	}
	if e = tx.Exec("INSERT INTO batch_requests(parent_job_id,plan_id,accepted_at,request_digest,body,profile_watermark,intent_watermark,heads_json,scope_key,state) VALUES(?,?,?,?,?,?,?,?,?,'accepted')", job.ID, p.ID, job.CreatedAt, requestDigest(p.Body), string(body), profiles, intents, string(hbody), key).Error; e != nil {
		return e
	}
	for i, part := range batchParts(spec) {
		raw, e := marshalBounded(part, 1<<20)
		if e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO batch_segments(parent_job_id,ordinal,resources_json,status) VALUES(?,?,?,'pending')", job.ID, i+1, string(raw)).Error; e != nil {
			return e
		}
	}
	return nil
}

func readBatchRow(tx *gorm.DB, parentID string) (batchRow, BatchRequest, error) {
	var row batchRow
	r := tx.Raw("SELECT * FROM batch_requests WHERE parent_job_id=?", parentID).Scan(&row)
	if r.Error != nil {
		return row, BatchRequest{}, r.Error
	}
	if r.RowsAffected != 1 {
		return row, BatchRequest{}, ErrNotFound
	}
	var value BatchRequest
	if json.Unmarshal([]byte(row.Body), &value) != nil || value.ParentJobID != parentID || value.PlanID != row.PlanID {
		return row, value, ErrInputInvalid
	}
	value.Status, value.ErrorCode = row.State, row.ErrorCode
	return row, value, nil
}
func readBatchSegments(tx *gorm.DB, parentID string) ([]BatchSegment, error) {
	var rows []batchSegmentRow
	if e := tx.Raw("SELECT * FROM batch_segments WHERE parent_job_id=? ORDER BY ordinal", parentID).Scan(&rows).Error; e != nil {
		return nil, e
	}
	out := []BatchSegment{}
	for _, r := range rows {
		v := BatchSegment{Ordinal: r.Ordinal, ChildJobID: r.ChildJobID, Status: r.Status, ErrorCode: r.ErrorCode, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, SnapshotID: r.SnapshotID, AuditRef: r.AuditRef, ReportRef: r.ReportRef, ExecutionMillis: r.ExecutionMillis, PhaseSource: r.PhaseSource}
		if json.Unmarshal([]byte(r.ResourcesJSON), &v.Resources) != nil || json.Unmarshal([]byte(r.PhaseJSON), &v.Phases) != nil {
			return nil, ErrInputInvalid
		}
		if r.ManifestJSON != "" {
			var m domain.InputManifest
			if json.Unmarshal([]byte(r.ManifestJSON), &m) != nil {
				return nil, ErrInputInvalid
			}
			v.Manifest = &m
		}
		out = append(out, v)
	}
	return out, nil
}
func batchAttemptJobIDs(tx *gorm.DB, rootID string) ([]string, error) {
	ids := []string{rootID}
	var rows []struct{ ID string }
	if e := tx.Raw("SELECT child_job_id AS id FROM batch_segments WHERE parent_job_id=? AND child_job_id IS NOT NULL ORDER BY ordinal", rootID).Scan(&rows).Error; e != nil {
		return nil, e
	}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}
func batchUsage(tx *gorm.DB, parentID string) (BatchUsage, error) {
	var out BatchUsage
	out.UnknownReasons = []string{}
	ids, e := batchAttemptJobIDs(tx, parentID)
	if e != nil {
		return out, e
	}
	var physical struct{ CloudAttempts, ModelAttempts int64 }
	if e = tx.Raw("SELECT COALESCE(sum(CASE WHEN bucket='cloud' THEN 1 ELSE 0 END),0) AS cloud_attempts,COALESCE(sum(CASE WHEN bucket='model' THEN 1 ELSE 0 END),0) AS model_attempts FROM outbound_attempts WHERE job_id IN ?", ids).Scan(&physical).Error; e != nil {
		return out, e
	}
	out.CloudAttempts, out.ModelAttempts = physical.CloudAttempts, physical.ModelAttempts
	var logical struct{ ModelCalls, ToolCalls int64 }
	if e = tx.Raw("SELECT COALESCE(sum(json_extract(a.body,'$.model_calls')),0) AS model_calls,COALESCE(sum(json_extract(a.body,'$.tool_calls')),0) AS tool_calls FROM job_agent_results a WHERE a.job_id IN ?", ids).Scan(&logical).Error; e != nil {
		return out, e
	}
	out.ModelCalls, out.ToolCalls = logical.ModelCalls, logical.ToolCalls
	segments, e := readBatchSegments(tx, parentID)
	if e != nil {
		return out, e
	}
	for _, v := range segments {
		out.ExecutionMillis += v.ExecutionMillis
		out.Phases.CollectionMillis += v.Phases.CollectionMillis
		out.Phases.RulesMillis += v.Phases.RulesMillis
		out.Phases.ModelMillis += v.Phases.ModelMillis
		out.Phases.FinalizeMillis += v.Phases.FinalizeMillis
	}
	var uncertain []struct {
		ID           string
		AgentID      string
		ModelReads   int64
		AgentStarted int64
	}
	if e = tx.Raw("SELECT j.id,COALESCE(a.id,'') AS agent_id,(SELECT count(*) FROM outbound_attempts o WHERE o.job_id=j.id AND o.bucket='model') AS model_reads,(SELECT count(*) FROM e0_events v WHERE v.job_id=j.id AND (v.type IN ('tool_started','tool_finished') OR (v.type='stage_changed' AND json_extract(v.payload,'$.stage')='agent_started'))) AS agent_started FROM e0_jobs j JOIN batch_segments b ON b.child_job_id=j.id LEFT JOIN job_agent_results a ON a.job_id=j.id WHERE b.parent_job_id=?", parentID).Scan(&uncertain).Error; e != nil {
		return out, e
	}
	for _, v := range uncertain {
		if v.AgentID == "" && (v.ModelReads > 0 || v.AgentStarted > 0) {
			out.UsageUnknown = true
			out.UnknownReasons = append(out.UnknownReasons, "LOGICAL_USAGE_NOT_SAVED")
		}
	}
	for _, v := range segments {
		if v.PhaseSource == "unknown" {
			out.UsageUnknown = true
			out.UnknownReasons = append(out.UnknownReasons, "PHASE_USAGE_UNKNOWN")
		}
	}
	return out, nil
}
func batchRequest(tx *gorm.DB, parentID string) (BatchRequest, error) {
	_, value, e := readBatchRow(tx, parentID)
	if e != nil {
		return value, e
	}
	value.Segments, e = readBatchSegments(tx, parentID)
	if e != nil {
		return value, e
	}
	value.Covered, value.Failed, value.Unprocessed = []PreparedResource{}, []PreparedResource{}, []PreparedResource{}
	for _, v := range value.Segments {
		switch {
		case v.AuditRef != "" && v.Manifest != nil:
			value.Covered = append(value.Covered, v.Resources...)
		case v.ChildJobID != "" && terminal(v.Status):
			value.Failed = append(value.Failed, v.Resources...)
		default:
			value.Unprocessed = append(value.Unprocessed, v.Resources...)
		}
	}
	value.Usage, e = batchUsage(tx, parentID)
	return value, e
}
func (s *Store) GetBatchRequest(ctx context.Context, parentID string) (value BatchRequest, err error) {
	if !attemptAlias(parentID) {
		return value, ErrInputInvalid
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var e error; value, e = batchRequest(tx, parentID); return e })
	return
}

// Returns original authority for counters/context. Metadata reads may follow a
// terminal parent; outbound admission must separately require its running state.
func batchAuthority(tx *gorm.DB, job Job) (Job, error) {
	if job.ParentJobID == "" {
		return job, nil
	}
	parent, e := get(tx, job.ParentJobID)
	if e != nil {
		return parent, e
	}
	if parent.ParentJobID != "" || parent.ConversationID != job.ConversationID || parent.BudgetRef != job.BudgetRef || parent.Kind != "audit" || job.Kind != "audit" {
		return parent, ErrInputInvalid
	}
	var matches int64
	if e = tx.Raw("SELECT count(*) FROM batch_segments WHERE parent_job_id=? AND child_job_id=?", parent.ID, job.ID).Scan(&matches).Error; e != nil {
		return parent, e
	}
	if matches != 1 {
		return parent, ErrInputInvalid
	}
	p, e := planRecord(tx, parent.PlanID)
	if e != nil {
		return parent, e
	}
	accepted, _, e := readBatchRow(tx, parent.ID)
	if e != nil {
		return parent, e
	}
	if accepted.PlanID != p.ID || accepted.AcceptedAt != parent.CreatedAt || accepted.RequestDigest != requestDigest(p.Body) {
		return parent, ErrInputInvalid
	}
	child, e := planRecord(tx, job.PlanID)
	if e != nil {
		return parent, e
	}
	if p.Batch == nil || child.Batch != nil || child.DataSourceID != p.DataSourceID || child.ConfigRevision != p.ConfigRevision || child.PolicyScopeRef != p.PolicyScopeRef || child.BudgetRef != p.BudgetRef || child.Mode != p.Mode || child.ConsumedJobID != job.ID {
		return parent, ErrInputInvalid
	}
	for _, a := range []struct{ child, parent string }{{job.RootDeadline, parent.RootDeadline}, {job.ExecutionDeadline, parent.ExecutionDeadline}} {
		x, e1 := time.Parse(time.RFC3339Nano, a.child)
		y, e2 := time.Parse(time.RFC3339Nano, a.parent)
		if e1 != nil || e2 != nil || x.After(y) {
			return parent, ErrInputInvalid
		}
	}
	var selection struct{ ResourcesJSON string }
	if e = tx.Raw("SELECT resources_json FROM batch_segments WHERE parent_job_id=? AND child_job_id=?", parent.ID, job.ID).Scan(&selection).Error; e != nil {
		return parent, e
	}
	var resources []PreparedResource
	if json.Unmarshal([]byte(selection.ResourcesJSON), &resources) != nil {
		return parent, ErrInputInvalid
	}
	expected, e := batchChildTemplate(p, resources)
	if e != nil {
		return parent, e
	}
	if !reflect.DeepEqual(expected.Preparation, child.Preparation) || !reflect.DeepEqual(expected.Heads, child.Heads) || !reflect.DeepEqual(expected.PreparedInput, child.PreparedInput) {
		return parent, ErrInputInvalid
	}
	if child.Workload.ProfileRef != p.Workload.ProfileRef || child.Workload.Legacy != p.Workload.Legacy {
		return parent, ErrInputInvalid
	}
	a, b := reflect.ValueOf(child.Workload.Limits), reflect.ValueOf(p.Workload.Limits)
	for i := 0; i < a.NumField(); i++ {
		if a.Field(i).Int() < 0 || a.Field(i).Int() > b.Field(i).Int() {
			return parent, ErrInputInvalid
		}
	}
	if child.EffectCeiling.CloudHTTPAttempts > p.EffectCeiling.CloudHTTPAttempts || child.EffectCeiling.ModelHTTPAttempts > p.EffectCeiling.ModelHTTPAttempts || child.EffectCeiling.AllowCloudReads && !p.EffectCeiling.AllowCloudReads || child.EffectCeiling.AllowModelRequests && !p.EffectCeiling.AllowModelRequests {
		return parent, ErrInputInvalid
	}
	return parent, nil
}
func millisSeconds(ms int64) int {
	if ms <= 0 {
		return 0
	}
	return int((ms + 999) / 1000)
}
func remainingBatchLimits(p ExecutionPlan, used BatchUsage, size int) (workload.Resolved, EffectCeiling, bool) {
	result := p.Workload
	l := &result.Limits
	l.AssetsPerBatch = min(l.AssetsPerBatch, size)
	l.ExecutionSeconds = max(0, l.ExecutionSeconds-millisSeconds(used.ExecutionMillis))
	l.CollectionSeconds = max(0, l.CollectionSeconds-millisSeconds(used.Phases.CollectionMillis))
	l.RulesSeconds = max(0, l.RulesSeconds-millisSeconds(used.Phases.RulesMillis))
	l.ModelSeconds = max(0, l.ModelSeconds-millisSeconds(used.Phases.ModelMillis))
	l.FinalizeSeconds = max(0, l.FinalizeSeconds-millisSeconds(used.Phases.FinalizeMillis))
	l.CloudHTTPAttempts = max(0, min(l.CloudHTTPAttempts, p.EffectCeiling.CloudHTTPAttempts)-int(used.CloudAttempts))
	l.ModelHTTPAttempts = max(0, min(l.ModelHTTPAttempts, p.EffectCeiling.ModelHTTPAttempts)-int(used.ModelAttempts))
	l.ModelLogicalCalls = max(0, l.ModelLogicalCalls-int(used.ModelCalls))
	l.ToolCalls = max(0, l.ToolCalls-int(used.ToolCalls))
	l.ToolBatch = min(l.ToolBatch, l.ToolCalls)
	l.ModelRequestSeconds = min(l.ModelRequestSeconds, l.ModelSeconds)
	if l.ModelSeconds == 0 {
		l.ModelRequestSeconds, l.ModelLogicalCalls, l.ModelHTTPAttempts, l.ToolCalls, l.ToolBatch = 0, 0, 0, 0, 0
	}
	effects := p.EffectCeiling
	effects.CloudHTTPAttempts = l.CloudHTTPAttempts
	effects.ModelHTTPAttempts = l.ModelHTTPAttempts
	effects.AllowCloudReads = effects.AllowCloudReads && l.CloudHTTPAttempts > 0
	effects.AllowModelRequests = effects.AllowModelRequests && l.ModelHTTPAttempts > 0 && l.ModelLogicalCalls > 0 && l.ModelSeconds > 0
	enough := l.ExecutionSeconds > 0 && l.RulesSeconds > 0 && l.FinalizeSeconds > 0
	if p.Preparation != nil {
		enough = enough && effects.AllowCloudReads && l.CollectionSeconds > 0
	}
	if p.EffectCeiling.AllowModelRequests {
		enough = enough && effects.AllowModelRequests && (p.Workload.Limits.ToolCalls == 0 || l.ToolCalls > 0)
	}
	return result, effects, enough && l.Validate("audit") == nil
}

// StartNextBatchSegment is a local transaction, not a queue/worker operation.
// A false result means no segment remains or the parent's remaining protection
// cannot support another segment; pending objects then stay explicitly unprocessed.
func (s *Store) StartNextBatchSegment(ctx context.Context, parentID string) (child Job, found bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		parent, e := get(tx, parentID)
		if e != nil {
			return e
		}
		if parent.ParentJobID != "" || parent.Status != "running" {
			return ErrJobTerminal
		}
		row, request, e := readBatchRow(tx, parentID)
		if e != nil {
			return e
		}
		if !enum(row.State, "accepted", "running") {
			return nil
		}
		p, e := planRecord(tx, parent.PlanID)
		if e != nil {
			return e
		}
		if p.Batch == nil || p.ConsumedJobID != parent.ID || row.RequestDigest != requestDigest(p.Body) || row.AcceptedAt != parent.CreatedAt {
			return ErrInputInvalid
		}
		segments, e := readBatchSegments(tx, parentID)
		if e != nil {
			return e
		}
		for _, seg := range segments {
			if seg.ChildJobID != "" && !terminal(seg.Status) {
				job, e := get(tx, seg.ChildJobID)
				if e != nil {
					return e
				}
				if !terminal(job.Status) {
					return ErrBusy
				}
				if _, e = refreshBatchSegment(tx, &parent, seg.Ordinal); e != nil {
					return e
				}
			}
		}
		segments, e = readBatchSegments(tx, parentID)
		if e != nil {
			return e
		}
		var next *BatchSegment
		for i := range segments {
			if segments[i].Status == "pending" {
				next = &segments[i]
				break
			}
		}
		if next == nil {
			return nil
		}
		deadline, e := earlierBatchDeadline(parent)
		if e != nil {
			return e
		}
		if !time.Now().UTC().Add(time.Second).Before(deadline) {
			return stopBatch(tx, parent, "partial", "BATCH_DEADLINE_EXHAUSTED")
		}
		used, e := batchUsage(tx, parentID)
		if e != nil {
			return e
		}
		if used.UsageUnknown {
			return stopBatch(tx, parent, "partial", "BATCH_USAGE_UNKNOWN")
		}
		limits, effects, enough := remainingBatchLimits(p, used, len(next.Resources))
		if !enough {
			return stopBatch(tx, parent, "partial", "BATCH_LIMIT_EXHAUSTED")
		}
		template, e := batchChildTemplate(p, next.Resources)
		if e != nil {
			return e
		}
		template.Workload, template.EffectCeiling = limits, effects
		if template.Preparation != nil {
			if _, e = validatePreparationGrant(tx, template); e != nil {
				return e
			}
		}
		prepared, e := preparePlan(template, true)
		if e != nil {
			return e
		}
		childID, e := opaqueID("job")
		if e != nil {
			return e
		}
		prepared.ConsumedJobID = childID
		// The immutable body represents an accepted internal plan; its consumption
		// column is filled by insertPlan then the same transaction below.
		if e = insertBatchChildPlan(tx, prepared); e != nil {
			return e
		}
		child = Job{ID: childID, ParentJobID: parent.ID, ConversationID: parent.ConversationID, ClientRequestID: "batch_" + childID, RequestDigest: requestDigest([]any{parent.ID, next.Ordinal, row.RequestDigest}), Kind: "audit", PlanID: prepared.ID, Status: "running", CreatedAt: parent.CreatedAt, StartedAt: now(), QueueDeadline: parent.QueueDeadline, RootDeadline: deadline.Format(time.RFC3339Nano), ExecutionDeadline: deadline.Format(time.RFC3339Nano), BudgetRef: parent.BudgetRef, Workload: limits}
		if e = tx.Exec("INSERT INTO e0_jobs(id,parent_job_id,conversation_id,client_request_id,request_digest,kind,plan_id,status,created_at,started_at,queue_deadline,root_deadline,execution_deadline,budget_ref) VALUES(?,?,?,?,?,?,?,'running',?,?,?,?,?,?)", child.ID, parent.ID, child.ConversationID, child.ClientRequestID, child.RequestDigest, child.Kind, child.PlanID, child.CreatedAt, child.StartedAt, child.QueueDeadline, child.RootDeadline, child.ExecutionDeadline, child.BudgetRef).Error; e != nil {
			return e
		}
		var parentTurn ConversationTurn
		r := tx.Raw("SELECT * FROM conversation_turns WHERE job_id=?", parent.ID).Scan(&parentTurn)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrInputInvalid
		}
		turnID, e := opaqueID("turn")
		if e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO conversation_turns(id,conversation_id,plan_id,job_id,message_id,state,created_at) VALUES(?,?,?,?,?,'running',?)", turnID, child.ConversationID, child.PlanID, child.ID, parentTurn.MessageID, parent.CreatedAt).Error; e != nil {
			return e
		}
		if prepared.Preparation != nil {
			if e = tx.Exec("INSERT INTO prepared_audit_acceptance(job_id,plan_id,accepted_at,profile_watermark,intent_watermark,scope_key,heads_json,request_digest) VALUES(?,?,?,?,?,?,?,?)", child.ID, prepared.ID, parent.CreatedAt, row.ProfileWatermark, row.IntentWatermark, row.ScopeKey, row.HeadsJSON, requestDigest(prepared.Body)).Error; e != nil {
				return e
			}
		}
		if e = tx.Exec("UPDATE batch_segments SET child_job_id=?,status='running',started_at=? WHERE parent_job_id=? AND ordinal=? AND status='pending'", child.ID, child.StartedAt, parent.ID, next.Ordinal).Error; e != nil {
			return e
		}
		if e = tx.Exec("UPDATE batch_requests SET state='running' WHERE parent_job_id=?", parent.ID).Error; e != nil {
			return e
		}
		if e = appendEvent(tx, &child, "job_status", map[string]any{"status": "running", "parent_job_ref": parent.ID, "batch_ordinal": next.Ordinal}); e != nil {
			return e
		}
		if e = appendEvent(tx, &parent, "stage_changed", map[string]any{"phase": "L1", "stage": "batch_segment_started", "child_job_ref": child.ID, "batch_ordinal": next.Ordinal}); e != nil {
			return e
		}
		found = true
		_ = request
		return nil
	})
	return
}
func earlierBatchDeadline(parent Job) (time.Time, error) {
	a, e1 := time.Parse(time.RFC3339Nano, parent.RootDeadline)
	b, e2 := time.Parse(time.RFC3339Nano, parent.ExecutionDeadline)
	if e1 != nil || e2 != nil {
		return time.Time{}, ErrInputInvalid
	}
	if b.Before(a) {
		a = b
	}
	return a, nil
}

// Internal plans derive from the accepted parent, not a new conversation/head
// admission. A later goal, archive or business confirmation cannot rewrite them.
func insertBatchChildPlan(tx *gorm.DB, p ExecutionPlan) error {
	var snap any
	if p.SnapshotID != "" {
		snap = p.SnapshotID
	}
	return tx.Exec("INSERT INTO execution_plans(id,kind,conversation_id,data_source_id,policy_scope_ref,snapshot_id,mode,conversation_revision,created_at,expires_at,config_revision,budget_ref,body,prepared_json,heads_json,consumed_job_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", p.ID, p.Kind, p.ConversationID, p.DataSourceID, p.PolicyScopeRef, snap, p.Mode, p.ConversationRevision, p.CreatedAt, p.ExpiresAt, p.ConfigRevision, p.BudgetRef, p.Body, p.PreparedJSON, p.HeadsJSON, p.ConsumedJobID).Error
}

func deriveBatchPhases(tx *gorm.DB, job Job, p ExecutionPlan) (BatchPhaseUsage, int64, error) {
	var result BatchPhaseUsage
	start, e1 := time.Parse(time.RFC3339Nano, job.StartedAt)
	finish, e2 := time.Parse(time.RFC3339Nano, job.FinishedAt)
	if e1 != nil || e2 != nil || finish.Before(start) {
		return result, 0, ErrInputInvalid
	}
	phase := "rules"
	if p.Preparation != nil {
		phase = "collection"
	}
	last := start
	charge := func(until time.Time) {
		ms := until.Sub(last).Milliseconds()
		switch phase {
		case "collection":
			result.CollectionMillis += ms
		case "rules":
			result.RulesMillis += ms
		case "model":
			result.ModelMillis += ms
		case "finalize":
			result.FinalizeMillis += ms
		}
		last = until
	}
	var events []Event
	if e := tx.Raw("SELECT * FROM e0_events WHERE job_id=? AND type='stage_changed' ORDER BY seq", job.ID).Scan(&events).Error; e != nil {
		return result, 0, e
	}
	for _, event := range events {
		var payload struct {
			Stage string `json:"stage"`
		}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil {
			return result, 0, ErrInputInvalid
		}
		next := ""
		switch payload.Stage {
		case "rules_started":
			next = "rules"
		case "agent_started":
			next = "model"
		case "report_building":
			next = "finalize"
		}
		if next == "" {
			continue
		}
		at, e := time.Parse(time.RFC3339Nano, event.OccurredAt)
		if e != nil || at.Before(last) || at.After(finish) {
			return result, 0, ErrInputInvalid
		}
		charge(at)
		phase = next
	}
	charge(finish)
	total := finish.Sub(start).Milliseconds()
	sum := result.CollectionMillis + result.RulesMillis + result.ModelMillis + result.FinalizeMillis
	if sum < total {
		result.FinalizeMillis += total - sum
	}
	return result, total, nil
}
func refreshBatchSegment(tx *gorm.DB, parent *Job, ordinal int) (BatchSegment, error) {
	segments, e := readBatchSegments(tx, parent.ID)
	if e != nil {
		return BatchSegment{}, e
	}
	var segment *BatchSegment
	for i := range segments {
		if segments[i].Ordinal == ordinal {
			segment = &segments[i]
			break
		}
	}
	if segment == nil {
		return BatchSegment{}, ErrNotFound
	}
	if segment.ChildJobID == "" {
		return *segment, ErrInputInvalid
	}
	child, e := get(tx, segment.ChildJobID)
	if e != nil {
		return *segment, e
	}
	if _, e = batchAuthority(tx, child); e != nil {
		return *segment, e
	}
	if !terminal(child.Status) {
		return *segment, ErrBusy
	}
	if terminal(segment.Status) {
		return *segment, nil
	}
	p, e := executionPlanForJob(tx, child)
	if e != nil {
		return *segment, e
	}
	segment.Status, segment.ErrorCode, segment.FinishedAt = child.Status, child.ErrorCode, child.FinishedAt
	segment.AuditRef = child.ResultRef
	input, e := jobInput(tx, child.ID)
	if e == nil {
		m := manifestFor(input)
		segment.Manifest = &m
		segment.SnapshotID = p.SnapshotID
	} else if !errors.Is(e, ErrInputNotSealed) {
		return *segment, e
	}
	if child.ResultRef != "" {
		run, e := auditRun(tx, child.ResultRef)
		if e != nil {
			return *segment, e
		}
		if segment.Manifest == nil || !reflect.DeepEqual(run.Result.Manifest, *segment.Manifest) {
			return *segment, ErrInputInvalid
		}
	}
	var publication struct{ ReportRef string }
	if e = tx.Raw("SELECT json_extract(payload,'$.report_ref') AS report_ref FROM e0_events WHERE job_id=? AND type='report_available' ORDER BY seq LIMIT 1", child.ID).Scan(&publication).Error; e != nil {
		return *segment, e
	}
	if publication.ReportRef != "" {
		report, e := reportRecord(tx, publication.ReportRef)
		if e != nil {
			return *segment, e
		}
		if report.AuditID != segment.AuditRef {
			return *segment, ErrInputInvalid
		}
		segment.ReportRef = report.ID
	}
	if segment.PhaseSource == "" {
		segment.Phases, segment.ExecutionMillis, e = deriveBatchPhases(tx, child, p)
		if e != nil {
			return *segment, e
		}
		segment.PhaseSource = "persisted_stage_windows"
		var stages []struct{ Stage string }
		if e = tx.Raw("SELECT json_extract(payload,'$.stage') AS stage FROM e0_events WHERE job_id=? AND type='stage_changed'", child.ID).Scan(&stages).Error; e != nil {
			return *segment, e
		}
		known := map[string]bool{}
		for _, v := range stages {
			known[v.Stage] = true
		}
		var modelCalls int64
		if e = tx.Raw("SELECT COALESCE(sum(json_extract(body,'$.model_calls')),0) FROM job_agent_results WHERE job_id=?", child.ID).Scan(&modelCalls).Error; e != nil {
			return *segment, e
		}
		if segment.AuditRef != "" && !known["rules_started"] || segment.ReportRef != "" && !known["report_building"] || modelCalls > 0 && !known["agent_started"] {
			segment.PhaseSource = "unknown"
		}
	}
	manifest := ""
	if segment.Manifest != nil {
		raw, e := marshalBounded(segment.Manifest, 32<<10)
		if e != nil {
			return *segment, e
		}
		manifest = string(raw)
	}
	phases, _ := json.Marshal(segment.Phases)
	if e = tx.Exec("UPDATE batch_segments SET status=?,error_code=?,finished_at=?,snapshot_id=?,audit_ref=?,report_ref=?,manifest_json=?,phase_json=?,phase_source=?,execution_millis=? WHERE parent_job_id=? AND ordinal=? AND status='running'", segment.Status, segment.ErrorCode, segment.FinishedAt, segment.SnapshotID, segment.AuditRef, segment.ReportRef, manifest, string(phases), segment.PhaseSource, segment.ExecutionMillis, parent.ID, ordinal).Error; e != nil {
		return *segment, e
	}
	if e = updateBatchState(tx, parent.ID); e != nil {
		return *segment, e
	}
	if e = appendEvent(tx, parent, "stage_changed", map[string]any{"stage": "batch_segment_finished", "batch_ordinal": ordinal, "child_job_ref": child.ID, "status": child.Status}); e != nil {
		return *segment, e
	}
	return *segment, nil
}
func (s *Store) RefreshBatchSegment(ctx context.Context, parentID string, ordinal int) (value BatchSegment, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		parent, e := get(tx, parentID)
		if e != nil {
			return e
		}
		value, e = refreshBatchSegment(tx, &parent, ordinal)
		return e
	})
	return
}
func updateBatchState(tx *gorm.DB, parentID string) error {
	segments, e := readBatchSegments(tx, parentID)
	if e != nil {
		return e
	}
	done, complete := len(segments) > 0, true
	for _, s := range segments {
		done = done && (terminal(s.Status) || s.Status == "unprocessed")
		complete = complete && s.Status == "completed" && s.Manifest != nil && s.AuditRef != "" && s.ReportRef != ""
	}
	if !done {
		return nil
	}
	status, code := "partial", "BATCH_PARTIAL"
	if complete {
		status, code = "completed", ""
	}
	return tx.Exec("UPDATE batch_requests SET state=?,error_code=?,finished_at=? WHERE parent_job_id=? AND state IN ('accepted','running')", status, code, now(), parentID).Error
}
func stopBatch(tx *gorm.DB, parent Job, status, code string) error {
	if !enum(status, "partial", "failed", "cancelled", "interrupted") || !validErrorCode(code) {
		return ErrInputInvalid
	}
	row, _, e := readBatchRow(tx, parent.ID)
	if e != nil {
		return e
	}
	if terminal(row.State) {
		if row.State == status && row.ErrorCode == code {
			return nil
		}
		return ErrConflict
	}
	segments, e := readBatchSegments(tx, parent.ID)
	if e != nil {
		return e
	}
	for _, seg := range segments {
		if seg.ChildJobID == "" || terminal(seg.Status) {
			continue
		}
		child, e := get(tx, seg.ChildJobID)
		if e != nil {
			return e
		}
		if !terminal(child.Status) {
			target := status
			if target == "partial" {
				target = "interrupted"
			}
			if target == "cancelled" {
				if e = tx.Exec("UPDATE e0_jobs SET status='cancelling' WHERE id=? AND status='running'", child.ID).Error; e != nil {
					return e
				}
				child.Status = "cancelling"
				target = "interrupted"
			}
			if e = finishJob(tx, &child, target, code); e != nil {
				return e
			}
		}
		if _, e = refreshBatchSegment(tx, &parent, seg.Ordinal); e != nil {
			return e
		}
	}
	if e = tx.Exec("UPDATE batch_segments SET status='unprocessed',error_code=?,finished_at=? WHERE parent_job_id=? AND status='pending'", code, now(), parent.ID).Error; e != nil {
		return e
	}
	if e = tx.Exec("UPDATE batch_requests SET state=?,error_code=?,finished_at=? WHERE parent_job_id=?", status, code, now(), parent.ID).Error; e != nil {
		return e
	}
	return appendEvent(tx, &parent, "stage_changed", map[string]any{"stage": "batch_request_stopped", "status": status, "error_code": code})
}
func (s *Store) StopBatch(ctx context.Context, parentID, status, code string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		parent, e := get(tx, parentID)
		if e != nil {
			return e
		}
		return stopBatch(tx, parent, status, code)
	})
}

// Exact server measurements may replace conservative stage windows once, before
// another segment starts. This is metadata only and cannot refund HTTP/logical use.
func (s *Store) RecordBatchPhaseUsage(ctx context.Context, childID string, usage BatchPhaseUsage) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, childID)
		if e != nil {
			return e
		}
		if job.ParentJobID == "" || !terminal(job.Status) {
			return ErrInputInvalid
		}
		if _, e = batchAuthority(tx, job); e != nil {
			return e
		}
		var row batchSegmentRow
		r := tx.Raw("SELECT * FROM batch_segments WHERE child_job_id=?", childID).Scan(&row)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrNotFound
		}
		start, e1 := time.Parse(time.RFC3339Nano, job.StartedAt)
		end, e2 := time.Parse(time.RFC3339Nano, job.FinishedAt)
		if e1 != nil || e2 != nil || end.Before(start) {
			return ErrInputInvalid
		}
		total := end.Sub(start).Milliseconds()
		sum := int64(0)
		for _, v := range []int64{usage.CollectionMillis, usage.RulesMillis, usage.ModelMillis, usage.FinalizeMillis} {
			if v < 0 || v > total-sum {
				return ErrInputInvalid
			}
			sum += v
		}
		usage.FinalizeMillis += total - sum
		raw, _ := json.Marshal(usage)
		if row.PhaseSource == "measured" {
			if row.PhaseJSON != string(raw) {
				return ErrConflict
			}
			return nil
		}
		var later int64
		if e = tx.Raw("SELECT count(*) FROM batch_segments WHERE parent_job_id=? AND ordinal>? AND child_job_id IS NOT NULL", job.ParentJobID, row.Ordinal).Scan(&later).Error; e != nil {
			return e
		}
		if later != 0 {
			return ErrConflict
		}
		return tx.Exec("UPDATE batch_segments SET phase_json=?,phase_source='measured',execution_millis=? WHERE child_job_id=?", string(raw), total, childID).Error
	})
}

// A parent has no synthetic merged audit. Completed requires each real segment
// to have its own sealed manifest, persisted audit and published fixed report.
func batchCompleteEvidence(tx *gorm.DB, job Job) bool {
	_, value, e := readBatchRow(tx, job.ID)
	if e != nil || value.Status != "completed" {
		return false
	}
	segments, e := readBatchSegments(tx, job.ID)
	if e != nil || len(segments) == 0 {
		return false
	}
	covered := 0
	for _, v := range segments {
		if v.Status != "completed" || v.Manifest == nil || v.AuditRef == "" || v.ReportRef == "" {
			return false
		}
		child, e := get(tx, v.ChildJobID)
		if e != nil || child.Status != "completed" || child.ParentJobID != job.ID || child.ResultRef != v.AuditRef {
			return false
		}
		report, e := reportRecord(tx, v.ReportRef)
		if e != nil || report.AuditID != v.AuditRef {
			return false
		}
		covered += len(v.Resources)
	}
	return covered == len(value.Spec.Resources)
}

func batchMessageUnique(tx *gorm.DB) (bool, error) {
	var indexes []struct {
		Name     string
		IsUnique int
	}
	if e := tx.Raw("SELECT name,\"unique\" AS is_unique FROM pragma_index_list('conversation_turns')").Scan(&indexes).Error; e != nil {
		return false, e
	}
	for _, index := range indexes {
		if index.IsUnique == 0 {
			continue
		}
		var cols []struct{ Name string }
		if e := tx.Raw("SELECT name FROM pragma_index_info(?)", index.Name).Scan(&cols).Error; e != nil {
			return false, e
		}
		if len(cols) == 1 && cols[0].Name == "message_id" {
			return true, nil
		}
	}
	return false, nil
}
func migrateBatchTurns(tx *gorm.DB) error {
	rebuild, e := batchMessageUnique(tx)
	if e != nil {
		return e
	}
	if !rebuild {
		return nil
	}
	for _, q := range []string{
		`CREATE TABLE batch_migrating_turns(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),plan_id TEXT UNIQUE NOT NULL REFERENCES execution_plans(id),job_id TEXT UNIQUE NOT NULL REFERENCES e0_jobs(id),message_id TEXT NOT NULL REFERENCES conversation_messages(id),reply_message_id TEXT REFERENCES conversation_messages(id),state TEXT NOT NULL CHECK(state IN ('queued','running','completed','partial','failed','cancelled','interrupted')),created_at TEXT NOT NULL)`,
		`INSERT INTO batch_migrating_turns SELECT id,conversation_id,plan_id,job_id,message_id,reply_message_id,state,created_at FROM conversation_turns`,
		`DROP TABLE conversation_turns`,
		`ALTER TABLE batch_migrating_turns RENAME TO conversation_turns`,
	} {
		if e := tx.Exec(q).Error; e != nil {
			return e
		}
	}
	return nil
}

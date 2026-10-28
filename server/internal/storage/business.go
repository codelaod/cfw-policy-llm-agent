package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

var ErrInputInvalid = errors.New("invalid business input")
var ErrVersionConflict = errors.New("business revision conflict")

type SnapshotRecord struct {
	ID         string       `json:"id"`
	ScopeRef   string       `json:"policy_scope_ref"`
	SourceKind string       `json:"source_kind"`
	CreatedAt  string       `json:"created_at"`
	Input      domain.Input `json:"input" gorm:"-"`
	Body       string       `json:"-"`
}
type FindingRecord struct {
	ID         string         `json:"id"`
	AuditID    string         `json:"audit_id"`
	FindingKey string         `json:"finding_key"`
	Finding    domain.Finding `json:"finding" gorm:"-"`
	Body       string         `json:"-"`
}
type AuditRecord struct {
	ID         string          `json:"id"`
	SnapshotID string          `json:"snapshot_id"`
	CreatedAt  string          `json:"created_at"`
	Result     domain.Result   `json:"result" gorm:"-"`
	Findings   []FindingRecord `json:"findings" gorm:"-"`
	Body       string          `json:"-"`
	Input      domain.Input    `json:"input" gorm:"-"`
	InputBody  string          `json:"-"`
}
type ReviewRequest struct {
	ClientRequestID  string `json:"-"`
	ExpectedRevision int64  `json:"expected_review_revision"`
	Kind             string `json:"kind"`
	Plan             string `json:"plan,omitempty"`
	Comment          string `json:"comment,omitempty"`
	Reason           string `json:"reason,omitempty"`
}
type ReviewAction struct {
	ID            string `json:"action_id"`
	FindingID     string `json:"finding_id"`
	Revision      int64  `json:"review_revision"`
	Kind          string `json:"kind"`
	Plan          string `json:"plan"`
	Comment       string `json:"comment"`
	Reason        string `json:"reason"`
	SavedAt       string `json:"saved_at"`
	RequestKey    string `json:"-"`
	RequestDigest string `json:"-"`
}
type ReviewHead struct {
	FindingID  string `json:"finding_id"`
	FindingKey string `json:"finding_key"`
	Revision   int64  `json:"review_revision"`
	Plan       string `json:"plan"`
}
type ReportRecord struct {
	ID            string       `json:"id"`
	AuditID       string       `json:"audit_id"`
	Format        string       `json:"format"`
	CreatedAt     string       `json:"created_at"`
	Reviews       []ReviewHead `json:"reviews" gorm:"-"`
	Content       []byte       `json:"-"`
	ReviewJSON    string       `json:"-"`
	RequestKey    string       `json:"-"`
	RequestDigest string       `json:"-"`
}

func migrateBusiness(tx *gorm.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS business_schema(version INTEGER PRIMARY KEY CHECK(version=1))`,
		`INSERT OR IGNORE INTO business_schema VALUES(1)`,
		`CREATE TABLE IF NOT EXISTS snapshots(id TEXT PRIMARY KEY,scope_ref TEXT NOT NULL,source_kind TEXT NOT NULL CHECK(source_kind IN ('synthetic','imported','live_readonly')),created_at TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)))`,
		`CREATE TABLE IF NOT EXISTS audit_runs(id TEXT PRIMARY KEY,snapshot_id TEXT NOT NULL REFERENCES snapshots(id),created_at TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)))`,
		`CREATE TABLE IF NOT EXISTS findings(id TEXT PRIMARY KEY,audit_id TEXT NOT NULL REFERENCES audit_runs(id),finding_key TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),UNIQUE(audit_id,finding_key))`,
		`CREATE TABLE IF NOT EXISTS review_heads(finding_id TEXT PRIMARY KEY REFERENCES findings(id),revision INTEGER NOT NULL DEFAULT 0 CHECK(revision>=0),plan TEXT NOT NULL DEFAULT '' CHECK(plan IN ('','todo','deferred')))`,
		`CREATE TABLE IF NOT EXISTS review_actions(id TEXT PRIMARY KEY,finding_id TEXT NOT NULL REFERENCES findings(id),revision INTEGER NOT NULL CHECK(revision>0),kind TEXT NOT NULL CHECK(kind IN ('comment','plan_change')),plan TEXT NOT NULL CHECK(plan IN ('','todo','deferred')),comment TEXT NOT NULL,reason TEXT NOT NULL,saved_at TEXT NOT NULL,request_key TEXT UNIQUE NOT NULL,request_digest TEXT NOT NULL,UNIQUE(finding_id,revision))`,
		`CREATE TABLE IF NOT EXISTS reports(id TEXT PRIMARY KEY,audit_id TEXT NOT NULL REFERENCES audit_runs(id),format TEXT NOT NULL CHECK(format IN ('json','markdown')),created_at TEXT NOT NULL,review_json TEXT NOT NULL CHECK(json_valid(review_json)),content BLOB NOT NULL,request_key TEXT UNIQUE NOT NULL,request_digest TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS audit_rechecks(id TEXT PRIMARY KEY,old_audit_id TEXT NOT NULL REFERENCES audit_runs(id),new_audit_id TEXT NOT NULL REFERENCES audit_runs(id),created_at TEXT NOT NULL,comparison TEXT NOT NULL CHECK(json_valid(comparison)),UNIQUE(old_audit_id,new_audit_id))`,
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return migrateExecution(tx)
}

func opaqueID(prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(bytes[:]), nil
}
func marshalBounded(value any, max int) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) > max {
		return nil, ErrInputInvalid
	}
	return data, nil
}
func requestDigest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func pageBounds(offset, limit int) error {
	if offset < 0 || offset > 10000 || limit < 1 || limit > 100 {
		return ErrInputInvalid
	}
	return nil
}
func validKey(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 128 }

func (s *Store) SaveSnapshot(ctx context.Context, in domain.Input) (record SnapshotRecord, err error) {
	if in.Scope.Ref == "" || (in.SourceKind != "synthetic" && in.SourceKind != "imported" && in.SourceKind != "live_readonly") {
		return record, ErrInputInvalid
	}
	body, err := marshalBounded(in, 5<<20)
	if err != nil {
		return record, err
	}
	id, err := opaqueID("snapshot")
	if err != nil {
		return record, err
	}
	record = SnapshotRecord{ID: id, ScopeRef: in.Scope.Ref, SourceKind: in.SourceKind, CreatedAt: now(), Body: string(body)}
	err = s.db.WithContext(ctx).Exec("INSERT INTO snapshots(id,scope_ref,source_kind,created_at,body) VALUES(?,?,?,?,?)", id, record.ScopeRef, record.SourceKind, record.CreatedAt, record.Body).Error
	if err == nil {
		err = json.Unmarshal(body, &record.Input)
	}
	return
}
func snapshot(tx *gorm.DB, id string) (record SnapshotRecord, err error) {
	r := tx.Raw("SELECT * FROM snapshots WHERE id=?", id).Scan(&record)
	if r.Error != nil {
		return record, r.Error
	}
	if r.RowsAffected == 0 {
		return record, ErrNotFound
	}
	err = json.Unmarshal([]byte(record.Body), &record.Input)
	return
}
func (s *Store) GetSnapshot(ctx context.Context, id string) (SnapshotRecord, error) {
	return snapshot(s.db.WithContext(ctx), id)
}
func (s *Store) ListSnapshots(ctx context.Context, offset, limit int) (records []SnapshotRecord, err error) {
	if err = pageBounds(offset, limit); err != nil {
		return
	}
	records = []SnapshotRecord{}
	err = s.db.WithContext(ctx).Raw("SELECT * FROM snapshots ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", limit, offset).Scan(&records).Error
	if err != nil {
		return
	}
	for i := range records {
		if err = json.Unmarshal([]byte(records[i].Body), &records[i].Input); err != nil {
			return
		}
	}
	return
}

func manifestFor(in domain.Input) domain.InputManifest {
	return domain.InputManifest{SnapshotVersion: in.SnapshotVersion, ProfileVersion: in.ProfileVersion, IntentVersion: in.IntentVersion, BaselineVersion: in.Baseline.Version, NormalizerVersion: in.NormalizerVersion, KernelVersion: in.KernelVersion, ProviderSemantics: in.Semantics, PolicyScope: in.Scope, EvaluationAt: in.EvaluationAt, CollectedAt: in.CollectedAt, SourceKind: in.SourceKind}
}

func (s *Store) SaveAudit(ctx context.Context, snapshotID string, result domain.Result) (record AuditRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base, e := snapshot(tx, snapshotID)
		if e != nil {
			return e
		}
		record, e = saveAuditInput(tx, snapshotID, base.Input, result)
		return e
	})
	return
}

func (s *Store) SaveAuditInput(ctx context.Context, snapshotID string, input domain.Input, result domain.Result) (record AuditRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		record, e = saveAuditInput(tx, snapshotID, input, result)
		return e
	})
	return
}

func saveAuditInput(tx *gorm.DB, snapshotID string, input domain.Input, result domain.Result) (record AuditRecord, err error) {
	source, err := snapshot(tx, snapshotID)
	if err != nil {
		return record, err
	}
	if !validResolvedConfiguration(source.Input, input) || !reflect.DeepEqual(manifestFor(input), result.Manifest) || validateFrozenResult(input, result) != nil {
		return record, ErrInputInvalid
	}
	body, err := marshalBounded(result, 8<<20)
	if err != nil {
		return record, err
	}
	inputBody, err := marshalBounded(input, 5<<20)
	if err != nil {
		return record, err
	}
	id, err := opaqueID("audit")
	if err != nil {
		return record, err
	}
	record = AuditRecord{ID: id, SnapshotID: snapshotID, CreatedAt: now(), Body: string(body), InputBody: string(inputBody), Findings: []FindingRecord{}}
	if err = tx.Exec("INSERT INTO audit_runs(id,snapshot_id,created_at,body,input_body) VALUES(?,?,?,?,?)", id, snapshotID, record.CreatedAt, record.Body, record.InputBody).Error; err != nil {
		return record, err
	}
	for _, finding := range result.Findings {
		fid, e := opaqueID("finding")
		if e != nil {
			return record, e
		}
		encoded, e := marshalBounded(finding, 2<<20)
		if e != nil {
			return record, e
		}
		if e = tx.Exec("INSERT INTO findings(id,audit_id,finding_key,body) VALUES(?,?,?,?)", fid, id, finding.ID, string(encoded)).Error; e != nil {
			return record, e
		}
		if e = tx.Exec("INSERT INTO review_heads(finding_id,revision,plan) VALUES(?,0,'')", fid).Error; e != nil {
			return record, e
		}
		item := FindingRecord{ID: fid, AuditID: id, FindingKey: finding.ID, Body: string(encoded)}
		if e = json.Unmarshal(encoded, &item.Finding); e != nil {
			return record, e
		}
		record.Findings = append(record.Findings, item)
	}
	if err = json.Unmarshal(body, &record.Result); err != nil {
		return record, err
	}
	err = json.Unmarshal(inputBody, &record.Input)
	return
}
func loadFindings(tx *gorm.DB, auditID string) (records []FindingRecord, err error) {
	records = []FindingRecord{}
	err = tx.Raw("SELECT * FROM findings WHERE audit_id=? ORDER BY finding_key", auditID).Scan(&records).Error
	if err != nil {
		return
	}
	for i := range records {
		if err = json.Unmarshal([]byte(records[i].Body), &records[i].Finding); err != nil {
			return
		}
	}
	return
}
func auditRun(tx *gorm.DB, id string) (record AuditRecord, err error) {
	r := tx.Raw("SELECT * FROM audit_runs WHERE id=?", id).Scan(&record)
	if r.Error != nil {
		return record, r.Error
	}
	if r.RowsAffected == 0 {
		return record, ErrNotFound
	}
	if err = json.Unmarshal([]byte(record.Body), &record.Result); err != nil {
		return
	}
	if err = json.Unmarshal([]byte(record.InputBody), &record.Input); err != nil {
		return
	}
	record.Findings, err = loadFindings(tx, id)
	return
}
func (s *Store) GetAudit(ctx context.Context, id string) (record AuditRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var e error; record, e = auditRun(tx, id); return e })
	return
}
func (s *Store) ListAudits(ctx context.Context, offset, limit int) (records []AuditRecord, err error) {
	if err = pageBounds(offset, limit); err != nil {
		return
	}
	records = []AuditRecord{}
	err = s.db.WithContext(ctx).Raw("SELECT * FROM audit_runs ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", limit, offset).Scan(&records).Error
	if err != nil {
		return
	}
	for i := range records {
		if err = json.Unmarshal([]byte(records[i].Body), &records[i].Result); err != nil {
			return
		}
		if err = json.Unmarshal([]byte(records[i].InputBody), &records[i].Input); err != nil {
			return
		}
	}
	return
}
func findingRecord(tx *gorm.DB, id string) (record FindingRecord, err error) {
	r := tx.Raw("SELECT * FROM findings WHERE id=?", id).Scan(&record)
	if r.Error != nil {
		return record, r.Error
	}
	if r.RowsAffected == 0 {
		return record, ErrNotFound
	}
	err = json.Unmarshal([]byte(record.Body), &record.Finding)
	return
}
func (s *Store) GetFinding(ctx context.Context, id string) (FindingRecord, error) {
	return findingRecord(s.db.WithContext(ctx), id)
}

func validateReview(req ReviewRequest) error {
	if !validKey(req.ClientRequestID) || req.ExpectedRevision < 0 || len(req.Comment) > 4096 || len(req.Reason) > 4096 {
		return ErrInputInvalid
	}
	switch req.Kind {
	case "comment":
		if strings.TrimSpace(req.Comment) == "" || req.Plan != "" || req.Reason != "" {
			return ErrInputInvalid
		}
	case "plan_change":
		if req.Plan != "todo" && req.Plan != "deferred" {
			return ErrInputInvalid
		}
		if req.Plan == "deferred" && strings.TrimSpace(req.Reason) == "" {
			return ErrInputInvalid
		}
	default:
		return ErrInputInvalid
	}
	return nil
}
func (s *Store) Review(ctx context.Context, findingID string, req ReviewRequest) (action ReviewAction, err error) {
	if err = validateReview(req); err != nil {
		return
	}
	digest := requestDigest(struct {
		Finding string
		Request ReviewRequest
	}{findingID, req})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old ReviewAction
		r := tx.Raw("SELECT * FROM review_actions WHERE request_key=?", req.ClientRequestID).Scan(&old)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected > 0 {
			if old.RequestDigest != digest {
				return ErrConflict
			}
			action = old
			return nil
		}
		if _, e := findingRecord(tx, findingID); e != nil {
			return e
		}
		var head ReviewHead
		r = tx.Raw("SELECT * FROM review_heads WHERE finding_id=?", findingID).Scan(&head)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 || head.Revision != req.ExpectedRevision {
			return ErrVersionConflict
		}
		plan := head.Plan
		if req.Kind == "plan_change" {
			plan = req.Plan
		}
		id, e := opaqueID("review")
		if e != nil {
			return e
		}
		action = ReviewAction{ID: id, FindingID: findingID, Revision: head.Revision + 1, Kind: req.Kind, Plan: plan, Comment: req.Comment, Reason: req.Reason, SavedAt: now(), RequestKey: req.ClientRequestID, RequestDigest: digest}
		if e = tx.Exec("INSERT INTO review_actions(id,finding_id,revision,kind,plan,comment,reason,saved_at,request_key,request_digest) VALUES(?,?,?,?,?,?,?,?,?,?)", id, findingID, action.Revision, action.Kind, plan, action.Comment, action.Reason, action.SavedAt, action.RequestKey, action.RequestDigest).Error; e != nil {
			return e
		}
		r = tx.Exec("UPDATE review_heads SET revision=?,plan=? WHERE finding_id=? AND revision=?", action.Revision, plan, findingID, req.ExpectedRevision)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrVersionConflict
		}
		return nil
	})
	return
}
func (s *Store) ReviewHistory(ctx context.Context, id string, after int64, limit int) (actions []ReviewAction, err error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, ErrInputInvalid
	}
	actions = []ReviewAction{}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, e := findingRecord(tx, id); e != nil {
			return e
		}
		return tx.Raw("SELECT * FROM review_actions WHERE finding_id=? AND revision>? ORDER BY revision LIMIT ?", id, after, limit).Scan(&actions).Error
	})
	return
}
func headsFor(tx *gorm.DB, id string) (heads []ReviewHead, err error) {
	heads = []ReviewHead{}
	err = tx.Raw("SELECT h.*,f.finding_key FROM review_heads h JOIN findings f ON f.id=h.finding_id WHERE f.audit_id=? ORDER BY f.finding_key", id).Scan(&heads).Error
	return
}
func (s *Store) CaptureReportInput(ctx context.Context, id string) (audit AuditRecord, heads []ReviewHead, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		audit, e = auditRun(tx, id)
		if e != nil {
			return e
		}
		heads, e = headsFor(tx, id)
		return e
	})
	return
}
func reportRecord(tx *gorm.DB, id string) (record ReportRecord, err error) {
	r := tx.Raw("SELECT * FROM reports WHERE id=?", id).Scan(&record)
	if r.Error != nil {
		return record, r.Error
	}
	if r.RowsAffected == 0 {
		return record, ErrNotFound
	}
	err = json.Unmarshal([]byte(record.ReviewJSON), &record.Reviews)
	return
}
func (s *Store) SaveReport(ctx context.Context, auditID, format string, heads []ReviewHead, content []byte, key string) (record ReportRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		record, e = saveReport(tx, auditID, format, heads, content, key)
		return e
	})
	return
}

func saveReport(tx *gorm.DB, auditID, format string, heads []ReviewHead, content []byte, key string) (record ReportRecord, err error) {
	if !validKey(key) || (format != "json" && format != "markdown") {
		return record, ErrInputInvalid
	}
	digest := requestDigest([]string{auditID, format})
	err = func() error {
		var old ReportRecord
		r := tx.Raw("SELECT * FROM reports WHERE request_key=?", key).Scan(&old)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected > 0 {
			if old.RequestDigest != digest {
				return ErrConflict
			}
			var e error
			record, e = reportRecord(tx, old.ID)
			return e
		}
		if len(content) == 0 || len(content) > 5<<20 || (format == "json" && !json.Valid(content)) {
			return ErrInputInvalid
		}
		audit, e := auditRun(tx, auditID)
		if e != nil {
			return e
		}
		known := map[string]string{}
		for _, finding := range audit.Findings {
			known[finding.ID] = finding.FindingKey
		}
		if len(heads) != len(known) {
			return ErrInputInvalid
		}
		seen := map[string]bool{}
		for _, head := range heads {
			if known[head.FindingID] != head.FindingKey || head.FindingKey == "" || seen[head.FindingID] || head.Revision < 0 || (head.Plan != "" && head.Plan != "todo" && head.Plan != "deferred") {
				return ErrInputInvalid
			}
			seen[head.FindingID] = true
			if head.Revision == 0 {
				if head.Plan != "" {
					return ErrInputInvalid
				}
			} else {
				var historical ReviewAction
				r := tx.Raw("SELECT plan FROM review_actions WHERE finding_id=? AND revision=?", head.FindingID, head.Revision).Scan(&historical)
				if r.Error != nil {
					return r.Error
				}
				if r.RowsAffected != 1 || historical.Plan != head.Plan {
					return ErrInputInvalid
				}
			}
		}
		encoded, e := marshalBounded(heads, 1<<20)
		if e != nil {
			return e
		}
		id, e := opaqueID("report")
		if e != nil {
			return e
		}
		record = ReportRecord{ID: id, AuditID: auditID, Format: format, CreatedAt: now(), ReviewJSON: string(encoded), Content: append([]byte(nil), content...), RequestKey: key, RequestDigest: digest}
		if e = tx.Exec("INSERT INTO reports(id,audit_id,format,created_at,review_json,content,request_key,request_digest) VALUES(?,?,?,?,?,?,?,?)", id, auditID, format, record.CreatedAt, record.ReviewJSON, content, key, digest).Error; e != nil {
			return e
		}
		return json.Unmarshal(encoded, &record.Reviews)
	}()
	return
}
func (s *Store) GetReport(ctx context.Context, id string) (ReportRecord, error) {
	return reportRecord(s.db.WithContext(ctx), id)
}
func (s *Store) ListReports(ctx context.Context, auditID string, limit int) (records []ReportRecord, err error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInputInvalid
	}
	records = []ReportRecord{}
	query := "SELECT * FROM reports"
	args := []any{}
	if auditID != "" {
		query += " WHERE audit_id=?"
		args = append(args, auditID)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit)
	err = s.db.WithContext(ctx).Raw(query, args...).Scan(&records).Error
	if err != nil {
		return
	}
	for i := range records {
		if err = json.Unmarshal([]byte(records[i].ReviewJSON), &records[i].Reviews); err != nil {
			return
		}
	}
	return
}

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"gorm.io/gorm"
)

type CloudObservation struct {
	Round               int    `json:"round"`
	Operation           string `json:"operation"`
	Pages               int    `json:"pages"`
	StartedAt           string `json:"started_at"`
	FinishedAt          string `json:"finished_at"`
	EnumerationComplete bool   `json:"enumeration_complete"`
	SemanticSHA256      string `json:"semantic_sha256"`
}

// CloudRawArtifact is a private persistence envelope. Body uses []byte so JSON
// stores base64 and preserves the exact provider bytes and their SHA256.
type CloudRawArtifact struct {
	Round     int               `json:"round"`
	Operation string            `json:"operation"`
	Query     map[string]string `json:"query"`
	Body      []byte            `json:"body"`
	SHA256    string            `json:"sha256"`
}

// CloudContent is a controlled product projection. RawArtifacts are private
// provider artifacts, kept in a separate column and never read by public views.
// IdentityVerified records a successful check; no caller-identity response,
// credential, private endpoint or provider account number belongs in this body.
type CloudContent struct {
	JobID            string             `json:"job_id"`
	Kind             string             `json:"kind"`
	ScopeRef         string             `json:"policy_scope_ref"`
	SnapshotID       string             `json:"snapshot_id,omitempty"`
	SourceRef        string             `json:"-"`
	ConfigRevision   string             `json:"-"`
	IdentityVerified bool               `json:"identity_verified"`
	ProductReady     bool               `json:"product_ready"`
	Observations     []CloudObservation `json:"observations"`
	Limitations      []string           `json:"limitations"`
	RawArtifacts     json.RawMessage    `json:"-"`
}

type CloudRecord struct {
	ID             string       `json:"id"`
	JobID          string       `json:"job_id"`
	CreatedAt      string       `json:"created_at"`
	Content        CloudContent `json:"content" gorm:"-"`
	Body           string       `json:"-"`
	SourceRef      string       `json:"-"`
	ConfigRevision string       `json:"-"`
	RequestDigest  string       `json:"-"`
}

func cloudCode(value string) bool {
	if len(value) < 1 || len(value) > 80 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, c := range value {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func cloudProductOperation(value string) bool {
	return enum(value, "DescribeAssetList", "DescribeControlPolicy", "DescribeNatFirewallList", "DescribeNatFirewallControlPolicy", "DescribeAddressBook", "DescribeInstances", "DescribeSecurityGroupAttribute")
}

func uniqueCloudJSON(raw []byte) bool {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	items := 0
	var visit func(int) bool
	visit = func(depth int) bool {
		items++
		if depth > 64 || items > 500000 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, composite := token.(json.Delim)
		if !composite {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				text, ok := key.(string)
				text = strings.ToLower(text)
				if !ok || seen[text] || !visit(depth+1) {
					return false
				}
				seen[text] = true
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !visit(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !visit(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func privateCloudArtifacts(raw json.RawMessage) bool {
	if !uniqueCloudJSON(raw) {
		return false
	}
	var artifacts []CloudRawArtifact
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&artifacts) != nil || artifacts == nil || len(artifacts) > 100 {
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return false
	}
	var sensitiveKeys func(any) bool
	sensitiveKeys = func(value any) bool {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
				if enum(normalized, "accesskeyid", "accesskeysecret", "securitytoken", "authorization", "sessiontoken", "credentials") || sensitiveKeys(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if sensitiveKeys(child) {
					return true
				}
			}
		}
		return false
	}
	for _, artifact := range artifacts {
		if artifact.Round < 1 || artifact.Round > 2 || !cloudProductOperation(artifact.Operation) || len(artifact.Query) > 20 {
			return false
		}
		for key, value := range artifact.Query {
			if len(key) < 1 || len(key) > 64 || len(value) > 8192 || enum(strings.ToLower(key), "memberuid", "accesskeyid", "accesskeysecret", "securitytoken", "authorization") {
				return false
			}
		}
		var body map[string]any
		if !uniqueCloudJSON(artifact.Body) || json.Unmarshal(artifact.Body, &body) != nil || body == nil || sensitiveKeys(body) {
			return false
		}
		hash := sha256.Sum256(artifact.Body)
		if artifact.SHA256 != hex.EncodeToString(hash[:]) {
			return false
		}
	}
	return true
}

func normalizeCloud(content CloudContent) (CloudContent, error) {
	if !attemptAlias(content.JobID) || !enum(content.Kind, "collect", "connection_check") || !attemptAlias(content.ScopeRef) || !attemptAlias(content.SourceRef) || !attemptAlias(content.ConfigRevision) || content.SnapshotID != "" || !content.IdentityVerified || len(content.Observations) > 100 || len(content.Limitations) > 100 {
		return CloudContent{}, ErrInputInvalid
	}
	seen := map[string]bool{}
	limitations := []string{}
	for _, code := range content.Limitations {
		if !cloudCode(code) {
			return CloudContent{}, ErrInputInvalid
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		limitations = append(limitations, code)
	}
	content.Limitations = limitations
	productObserved := false
	for _, o := range content.Observations {
		if !cloudProductOperation(o.Operation) || o.Round < 1 || o.Round > 2 || o.Pages < 1 || o.Pages > 100 {
			return CloudContent{}, ErrInputInvalid
		}
		start, e1 := time.Parse(time.RFC3339Nano, o.StartedAt)
		finish, e2 := time.Parse(time.RFC3339Nano, o.FinishedAt)
		hash, e3 := hex.DecodeString(o.SemanticSHA256)
		if e1 != nil || e2 != nil || finish.Before(start) || e3 != nil || len(hash) != 32 || o.SemanticSHA256 != strings.ToLower(o.SemanticSHA256) {
			return CloudContent{}, ErrInputInvalid
		}
		productObserved = true
		if !o.EnumerationComplete && !seen["OBSERVATION_INCOMPLETE"] {
			content.Limitations = append(content.Limitations, "OBSERVATION_INCOMPLETE")
			seen["OBSERVATION_INCOMPLETE"] = true
		}
	}
	if content.ProductReady && !productObserved {
		return CloudContent{}, ErrInputInvalid
	}
	if content.Observations == nil {
		content.Observations = []CloudObservation{}
	}
	if content.Limitations == nil {
		content.Limitations = []string{}
	}
	if len(content.RawArtifacts) == 0 {
		content.RawArtifacts = json.RawMessage(`[]`)
	}
	if !json.Valid(content.RawArtifacts) || len(content.RawArtifacts) > 32<<20 || !privateCloudArtifacts(content.RawArtifacts) {
		return CloudContent{}, ErrInputInvalid
	}
	if content.Kind == "connection_check" {
		var artifacts []CloudRawArtifact
		if json.Unmarshal(content.RawArtifacts, &artifacts) != nil || len(artifacts) != 0 {
			return CloudContent{}, ErrInputInvalid
		}
	}
	if _, err := marshalBounded(content, 64<<10); err != nil {
		return CloudContent{}, err
	}
	// Detach all slices, including the private artifact buffer, from the caller.
	encoded, _ := json.Marshal(content)
	var copy CloudContent
	if json.Unmarshal(encoded, &copy) != nil {
		return CloudContent{}, ErrInputInvalid
	}
	copy.SourceRef, copy.ConfigRevision = content.SourceRef, content.ConfigRevision
	copy.RawArtifacts = append(json.RawMessage(nil), content.RawArtifacts...)
	return copy, nil
}

func jobCloudResult(tx *gorm.DB, jobID string) (*CloudRecord, error) {
	var record CloudRecord
	// raw_artifacts is deliberately absent even for protected API reads.
	r := tx.Raw("SELECT id,job_id,created_at,source_ref,config_revision,body,request_digest FROM job_cloud_results WHERE job_id=?", jobID).Scan(&record)
	if r.Error != nil {
		return nil, r.Error
	}
	if r.RowsAffected == 0 {
		return nil, nil
	}
	d := json.NewDecoder(strings.NewReader(record.Body))
	d.DisallowUnknownFields()
	if d.Decode(&record.Content) != nil {
		return nil, ErrInputInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF || record.Content.JobID != jobID {
		return nil, ErrInputInvalid
	}
	record.Content.SourceRef, record.Content.ConfigRevision = record.SourceRef, record.ConfigRevision
	return &record, nil
}

// FinalizeCloud uses one cancellable short transaction after all network work.
// It binds only to the accepted plan's frozen intent; the current conversation
// scope, archive flag and catalog activation do not replace that authority.
func (s *Store) FinalizeCloud(ctx context.Context, jobID string, content CloudContent, input *domain.Input) (record CloudRecord, err error) {
	if err = ctx.Err(); err != nil {
		return record, err
	}
	content, err = normalizeCloud(content)
	if err != nil || content.JobID != jobID {
		return record, ErrInputInvalid
	}
	var inputBody []byte
	if input != nil {
		if content.Kind != "collect" || input.SourceKind != "live_readonly" {
			return record, ErrInputInvalid
		}
		if e := audit.ValidateContext(ctx, *input); e != nil {
			if ctx.Err() != nil {
				return record, ctx.Err()
			}
			return record, ErrInputInvalid
		}
		for _, gap := range input.Completeness.KnownGaps {
			if !cloudCode(gap) {
				return record, ErrInputInvalid
			}
			content.Limitations = append(content.Limitations, gap)
		}
		if !input.Completeness.EnumerationComplete {
			content.Limitations = append(content.Limitations, "COLLECTION_INCOMPLETE")
		}
		if input.Completeness.ObservationConsistency != "observed_stable" {
			content.Limitations = append(content.Limitations, "CONFIGURATION_NOT_STABLE")
		}
		content, err = normalizeCloud(content)
		if err != nil {
			return record, err
		}
		inputBody, err = marshalBounded(*input, 5<<20)
		if err != nil {
			return record, err
		}
	} else if content.Kind == "collect" {
		return record, ErrInputInvalid
	}
	digest := requestDigest(struct {
		Content          CloudContent
		Source, Revision string
		Raw              json.RawMessage
		Input            json.RawMessage
	}{content, content.SourceRef, content.ConfigRevision, content.RawArtifacts, inputBody})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, e := get(tx, jobID)
		if e != nil {
			return e
		}
		old, e := jobCloudResult(tx, jobID)
		if e != nil {
			return e
		}
		if old != nil {
			if old.RequestDigest != digest {
				return ErrConflict
			}
			record = *old
			return nil
		}
		if job.Status != "running" {
			return ErrJobTerminal
		}
		if job.Kind != content.Kind || job.ResultRef != "" {
			return ErrInputInvalid
		}
		plan, e := planRecord(tx, job.PlanID)
		if e != nil {
			return e
		}
		if plan.ConsumedJobID != job.ID || plan.Kind != job.Kind || plan.ConversationID != job.ConversationID || plan.DataSourceID != content.SourceRef || plan.ConfigRevision != content.ConfigRevision || plan.PolicyScopeRef != content.ScopeRef || plan.PreparedInput.Scope.Ref != content.ScopeRef || plan.SnapshotID != "" || !plan.EffectCeiling.AllowCloudReads || plan.EffectCeiling.CloudHTTPAttempts < 1 || plan.EffectCeiling.AllowModelRequests {
			return ErrInputInvalid
		}
		if input != nil {
			var fixed domain.Input
			if json.Unmarshal(inputBody, &fixed) != nil || !reflect.DeepEqual(fixed.Scope, plan.PreparedInput.Scope) || !reflect.DeepEqual(fixed.Selection, plan.PreparedInput.Selection) || !reflect.DeepEqual(fixed.Baseline, plan.PreparedInput.Baseline) || plan.EvaluationAt != "" && fixed.EvaluationAt != plan.EvaluationAt {
				return ErrInputInvalid
			}
			snapshot, e := saveSnapshot(tx, fixed)
			if e != nil {
				return e
			}
			content.SnapshotID = snapshot.ID
			r := tx.Exec("UPDATE e0_jobs SET sealed_input=? WHERE id=? AND status='running' AND sealed_input IS NULL", string(inputBody), job.ID)
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return ErrConflict
			}
		}
		id, e := opaqueID("cloud_result")
		if e != nil {
			return e
		}
		body, e := marshalBounded(content, 64<<10)
		if e != nil {
			return e
		}
		record = CloudRecord{ID: id, JobID: job.ID, CreatedAt: now(), Content: content, Body: string(body), SourceRef: content.SourceRef, ConfigRevision: content.ConfigRevision, RequestDigest: digest}
		if e = tx.Exec("INSERT INTO job_cloud_results(id,job_id,created_at,source_ref,config_revision,body,raw_artifacts,request_digest) VALUES(?,?,?,?,?,?,?,?)", record.ID, job.ID, record.CreatedAt, content.SourceRef, content.ConfigRevision, record.Body, string(content.RawArtifacts), digest).Error; e != nil {
			return e
		}
		if e = appendEvent(tx, &job, "stage_changed", map[string]any{"phase": "L1", "stage": "cloud_result_saved", "cloud_result_ref": record.ID}); e != nil {
			return e
		}
		text := "已保存本次只读采集的固定资料；采集范围、完整性与局限以任务结果为准，云端配置由操作者手动修改。"
		if job.Kind == "connection_check" {
			text = "已核对本次显式调用身份；身份核对不代表产品权限、字段与完整性已经验证。"
		}
		message, replay, e := insertJobConversationMessage(tx, job.ID, MessageInput{ClientMessageID: "cloud_result_" + job.ID, Role: "event", Origin: "application", PublicText: text, PolicyScopeRef: plan.PolicyScopeRef, JobID: job.ID, RefKind: "job", RefID: job.ID})
		if e != nil {
			return e
		}
		if replay {
			return ErrConflict
		}
		update := tx.Exec("UPDATE conversation_turns SET reply_message_id=? WHERE job_id=? AND reply_message_id IS NULL", message.ID, job.ID)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrConflict
		}
		if e = appendEvent(tx, &job, "message_available", map[string]any{"message_ref": message.ID, "complete_message": true}); e != nil {
			return e
		}
		status, code := "completed", ""
		if len(content.Limitations) > 0 || !content.ProductReady {
			status, code = "partial", "CLOUD_LIMITED"
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		return finishJob(tx, &job, status, code)
	})
	if err != nil {
		record = CloudRecord{}
	} else {
		record.Content.RawArtifacts = nil
	}
	return
}

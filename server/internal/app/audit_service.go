package app

import (
	"context"
	"crypto/rand"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/report"
	"cfw-policy-llm-agent/server/internal/storage"
)

// AuditService orchestrates local immutable inputs and deterministic results.
// It has no cloud/model client and cannot acquire credentials or send HTTP.
type AuditService struct {
	Store *storage.Store
	rules func(context.Context, domain.Input) (domain.Result, error)
}

func NewAuditService(store *storage.Store) *AuditService {
	return &AuditService{Store: store, rules: audit.Run}
}

func DefaultBaseline() domain.Baseline {
	return audit.ProjectBaseline()
}

func (s *AuditService) Import(ctx context.Context, data []byte) (storage.SnapshotRecord, error) {
	if err := ctx.Err(); err != nil {
		return storage.SnapshotRecord{}, err
	}
	in, err := audit.DecodeImportContext(ctx, data)
	if err != nil {
		return storage.SnapshotRecord{}, err
	}
	// The imported configuration can declare business assumptions, but it cannot
	// change the server's adopted checks, port baseline, or approved exceptions.
	if !sameBaseline(in.Baseline, DefaultBaseline()) {
		return storage.SnapshotRecord{}, storage.ErrInputInvalid
	}
	in.Baseline = DefaultBaseline()
	in.SnapshotVersion = "snapshot_" + rand.Text()
	return s.Store.SaveSnapshot(ctx, in)
}

// LoadExample selects a server-owned constructor, never a caller supplied path
// or a file's self-declared provenance. Every result remains explicitly synthetic.
func (s *AuditService) LoadExample(ctx context.Context, name string) (storage.SnapshotRecord, error) {
	if err := ctx.Err(); err != nil {
		return storage.SnapshotRecord{}, err
	}
	in, err := audit.SyntheticExample(name)
	if err != nil {
		return storage.SnapshotRecord{}, err
	}
	if in.SourceKind != "synthetic" || !sameBaseline(in.Baseline, DefaultBaseline()) {
		return storage.SnapshotRecord{}, storage.ErrInputInvalid
	}
	if err = audit.Validate(in); err != nil {
		return storage.SnapshotRecord{}, err
	}
	return s.Store.SaveSnapshot(ctx, in)
}

func sameBaseline(actual, adopted domain.Baseline) bool {
	if actual.Version != adopted.Version || len(actual.Exceptions) != 0 || len(actual.EnabledChecks) != len(adopted.EnabledChecks) || len(actual.RiskPorts) != len(adopted.RiskPorts) {
		return false
	}
	checks := map[string]bool{}
	for _, check := range adopted.EnabledChecks {
		checks[check] = true
	}
	for _, check := range actual.EnabledChecks {
		if !checks[check] {
			return false
		}
		delete(checks, check)
	}
	ports := map[domain.RiskPort]bool{}
	for _, port := range adopted.RiskPorts {
		ports[port] = true
	}
	for _, port := range actual.RiskPorts {
		if !ports[port] {
			return false
		}
		delete(ports, port)
	}
	return len(checks) == 0 && len(ports) == 0
}

func (s *AuditService) RunRules(ctx context.Context, snapshotID string) (storage.AuditRecord, error) {
	if err := ctx.Err(); err != nil {
		return storage.AuditRecord{}, err
	}
	snapshot, err := s.Store.GetSnapshot(ctx, snapshotID)
	if err != nil {
		return storage.AuditRecord{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, runErr := s.rules(bounded, snapshot.Input)
	if runErr == nil {
		runErr = bounded.Err()
	}
	if runErr != nil && result.SchemaVersion != 1 {
		return storage.AuditRecord{}, runErr
	}
	if runErr != nil {
		result.Coverage.Complete = false
		result.Coverage.Limitations = append(append([]string(nil), result.Coverage.Limitations...), "RULE_STAGE_INTERRUPTED")
	}
	// Finite local finalization owns its service context even if cancellation
	// arrives while SQLite is waiting. This does not resume any computation or
	// HTTP; the caller still receives cancellation and decides task finality.
	saveCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finish()
	record, saveErr := s.Store.SaveAudit(saveCtx, snapshot.ID, result)
	if saveErr != nil {
		return storage.AuditRecord{}, saveErr
	}
	if runErr == nil {
		runErr = bounded.Err()
	}
	return record, runErr
}

func (s *AuditService) Review(ctx context.Context, findingID string, request storage.ReviewRequest) (storage.ReviewAction, error) {
	return s.Store.Review(ctx, findingID, request)
}

func (s *AuditService) Report(ctx context.Context, auditID, format, key string) (storage.ReportRecord, error) {
	job, found, err := s.Store.JobForAudit(ctx, auditID)
	if err != nil {
		return storage.ReportRecord{}, err
	}
	if !found {
		return s.buildReport(ctx, auditID, format, key, nil)
	}
	if !storage.Terminal(job.Status) {
		return storage.ReportRecord{}, storage.ErrJobTerminal
	}
	plan, err := s.Store.GetPlan(ctx, job.PlanID)
	if err != nil {
		return storage.ReportRecord{}, err
	}
	execution := report.Execution{Mode: plan.Mode, ModelStage: "not_requested"}
	if plan.Mode == "rules_and_agent" {
		execution.ModelStage = "not_completed"
		if job.ErrorCode == "MODEL_NOT_CONFIGURED" {
			execution.ModelStage = "not_configured"
		}
	}
	return s.ReportWithExecution(ctx, auditID, format, key, execution)
}

// ReportWithExecution is used by the local workflow after its rule/model
// stages have ended. HTTP exports use Report, which requires a terminal job.
func (s *AuditService) ReportWithExecution(ctx context.Context, auditID, format, key string, execution report.Execution) (storage.ReportRecord, error) {
	return s.buildReport(ctx, auditID, format, key, &execution)
}

func (s *AuditService) buildReport(ctx context.Context, auditID, format, key string, execution *report.Execution) (storage.ReportRecord, error) {
	heads, content, err := s.prepareReport(ctx, auditID, format, execution)
	if err != nil {
		return storage.ReportRecord{}, err
	}
	return s.Store.SaveReport(ctx, auditID, format, heads, content, key)
}

func (s *AuditService) prepareReport(ctx context.Context, auditID, format string, execution *report.Execution) ([]storage.ReviewHead, []byte, error) {
	run, heads, err := s.Store.CaptureReportInput(ctx, auditID)
	if err != nil {
		return nil, nil, err
	}
	publicHeads := make([]report.ReviewHead, 0, len(heads))
	for _, head := range heads {
		publicHeads = append(publicHeads, report.ReviewHead{FindingID: head.FindingKey, Revision: head.Revision, Plan: head.Plan})
	}
	var content []byte
	if execution == nil {
		content, err = report.Build(ctx, run.Result, publicHeads, format)
	} else {
		content, err = report.BuildWithExecution(ctx, run.Result, publicHeads, format, *execution)
	}
	if err != nil {
		return nil, nil, err
	}
	return heads, content, nil
}

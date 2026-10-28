package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"cfw-policy-llm-agent/server/internal/domain"
)

func storedCase() (domain.Input, domain.Result) {
	in := domain.Input{SchemaVersion: 1, SourceKind: "synthetic", SnapshotVersion: "snapshot-v1", ProfileVersion: "profile-v1", IntentVersion: "intent-v1", NormalizerVersion: "normalizer-v1", KernelVersion: "kernel-v1", CollectedAt: "2026-10-27T23:59:00Z", EvaluationAt: "2026-10-28T00:00:00Z", Scope: domain.Scope{Ref: "scope-in", ControlPlaneRef: "control-demo", IPFamily: "ipv4", Provider: "aliyun", AccountRef: "account-1", Boundary: "internet", Direction: "in", PolicySetRef: "policy-in"}, Baseline: domain.Baseline{Version: "baseline-v1", EnabledChecks: []string{"CHK-07"}}, Semantics: domain.Semantics{ProfileID: "synthetic-v1", Version: "1", DefaultAction: "accept", SourceRef: "synthetic"}, Selection: domain.Selection{Kind: "assets", AssetIDs: []string{"private-resource"}}, Assets: []domain.Asset{{ID: "private-resource", ScopeRef: "scope-in", Address: "192.0.2.10"}}}
	manifest := domain.InputManifest{SnapshotVersion: in.SnapshotVersion, ProfileVersion: in.ProfileVersion, IntentVersion: in.IntentVersion, BaselineVersion: in.Baseline.Version, NormalizerVersion: in.NormalizerVersion, KernelVersion: in.KernelVersion, ProviderSemantics: in.Semantics, PolicyScope: in.Scope, EvaluationAt: in.EvaluationAt, CollectedAt: in.CollectedAt, SourceKind: in.SourceKind}
	result := domain.Result{SchemaVersion: 1, Manifest: manifest, Findings: []domain.Finding{{ID: "finding-local-1", RuleID: "CHK-07", RuleVersion: in.Baseline.Version, Origin: "rule", ScopeRef: in.Scope.Ref, SubjectRef: "private-resource", Verdict: "fail", AssessmentBasis: "synthetic_assumption", Assumptions: []string{"人工固定案例，仅证明配置推导"}, Severity: "high", RemediationPriority: "R1", EvidenceRefs: []string{"e1"}}}, Evidence: []domain.Evidence{{ID: "e1", SnapshotVersion: in.SnapshotVersion, ScopeRef: in.Scope.Ref, SubjectRef: "private-resource", Field: "/provider_semantics/default_action", ObservedAt: in.CollectedAt}}, Coverage: domain.Coverage{Complete: true}}
	return in, result
}

func TestBusinessHistoryIsImmutableAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "business.sqlite")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	in, result := storedCase()
	snapshot, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := s.SaveAudit(ctx, snapshot.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	in.Assets[0].Address = "198.51.100.40"
	result.Findings[0].Verdict = "pass"
	got, err := s.GetSnapshot(ctx, snapshot.ID)
	if err != nil || got.Input.Assets[0].Address != "192.0.2.10" {
		t.Fatalf("input mutated after save: %+v %v", got, err)
	}
	run, err := s.GetAudit(ctx, audit.ID)
	if err != nil || run.Result.Findings[0].Verdict != "fail" {
		t.Fatal("raw rules changed after caller mutation")
	}
	finding := run.Findings[0]
	request := ReviewRequest{ClientRequestID: "review-1", ExpectedRevision: 0, Kind: "plan_change", Plan: "todo", Comment: "local private review"}
	first, err := s.Review(ctx, finding.ID, request)
	if err != nil || first.Revision != 1 {
		t.Fatalf("review: %+v %v", first, err)
	}
	replay, err := s.Review(ctx, finding.ID, request)
	if err != nil || replay.ID != first.ID {
		t.Fatal("idempotent replay must precede current revision check")
	}
	conflict := request
	conflict.Comment = "different"
	if _, err = s.Review(ctx, finding.ID, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key different content: %v", err)
	}
	stale := request
	stale.ClientRequestID = "review-2"
	if _, err = s.Review(ctx, finding.ID, stale); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	capture, heads, err := s.CaptureReportInput(ctx, audit.ID)
	if err != nil || len(heads) != 1 || heads[0].Revision != 1 || heads[0].FindingKey != "finding-local-1" {
		t.Fatalf("capture: %+v %v", heads, err)
	}
	for _, format := range []string{"json", "markdown"} {
		content := []byte(`{"public":"fixed"}`)
		if format == "markdown" {
			content = []byte("# Fixed public report\n")
		}
		report, err := s.SaveReport(ctx, capture.ID, format, heads, content, "report-"+format)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Review(ctx, finding.ID, ReviewRequest{ClientRequestID: "comment-" + format, ExpectedRevision: int64(lenMustHistory(t, s, finding.ID)), Kind: "comment", Comment: "must stay private"}); err != nil {
			t.Fatal(err)
		}
		again, newHeads, err := s.CaptureReportInput(ctx, audit.ID)
		if err != nil {
			t.Fatal(err)
		}
		old, err := s.SaveReport(ctx, again.ID, format, newHeads, []byte("newly generated body"), "report-"+format)
		if err != nil || old.ID != report.ID || string(old.Content) != string(content) || old.Reviews[0].Revision != 1 {
			t.Fatalf("report replay or fixed revision changed: %+v %v", old, err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	run, err = s.GetAudit(ctx, audit.ID)
	if err != nil || run.Result.Findings[0].Verdict != "fail" {
		t.Fatal("review changed original rule after restart")
	}
	history, err := s.ReviewHistory(ctx, finding.ID, 0, 100)
	if err != nil || len(history) != 3 {
		t.Fatalf("review history lost: %d %v", len(history), err)
	}
	reports, err := s.ListReports(ctx, audit.ID, 100)
	if err != nil || len(reports) != 2 {
		t.Fatal("fixed reports lost after restart")
	}
	for _, report := range reports {
		saved, err := s.GetReport(ctx, report.ID)
		if err != nil || !reflect.DeepEqual(saved.Content, report.Content) {
			t.Fatal("report content changed")
		}
	}
}

func lenMustHistory(t *testing.T, s *Store, id string) int {
	t.Helper()
	v, err := s.ReviewHistory(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(v)
}

func TestReviewCASFailureAndOwnership(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "business.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in, result := storedCase()
	snapshot, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	wrong := result
	wrong.Manifest.PolicyScope.Direction = "out"
	if _, err = s.SaveAudit(ctx, snapshot.ID, wrong); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("cross-scope manifest accepted")
	}
	_, result = storedCase()
	audit, err := s.SaveAudit(ctx, snapshot.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	id := audit.Findings[0].ID
	if err = s.db.Exec("CREATE TRIGGER fail_review BEFORE INSERT ON review_actions BEGIN SELECT RAISE(ABORT,'synthetic failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	request := ReviewRequest{ClientRequestID: "failed-key", ExpectedRevision: 0, Kind: "comment", Comment: "private"}
	if _, err = s.Review(ctx, id, request); err == nil {
		t.Fatal("failed insert was reported saved")
	}
	_, heads, err := s.CaptureReportInput(ctx, audit.ID)
	if err != nil || heads[0].Revision != 0 || lenMustHistory(t, s, id) != 0 {
		t.Fatal("review partial commit")
	}
	if err = s.db.Exec("DROP TRIGGER fail_review").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, id, request); err != nil {
		t.Fatal("same key must be reusable after rollback", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"race-a", "race-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := s.Review(ctx, id, ReviewRequest{ClientRequestID: key, ExpectedRevision: 1, Kind: "plan_change", Plan: "deferred", Reason: "needs local check"})
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("CAS outcomes success=%d conflicts=%d", success, conflicts)
	}
	if _, err = s.Review(ctx, id, ReviewRequest{ClientRequestID: "empty-reason", ExpectedRevision: 2, Kind: "plan_change", Plan: "deferred"}); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("defer without reason accepted")
	}
}

func TestReportRejectsFabricatedReviewHistory(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "forgery.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in, result := storedCase()
	snapshot, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := s.SaveAudit(ctx, snapshot.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	_, heads, err := s.CaptureReportInput(ctx, audit.ID)
	if err != nil {
		t.Fatal(err)
	}
	heads[0].Revision = 999
	heads[0].Plan = "deferred"
	if _, err = s.SaveReport(ctx, audit.ID, "json", heads, []byte(`{"public":true}`), "fabricated"); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("fabricated review version accepted: %v", err)
	}
	heads[0].Revision = 0
	heads[0].Plan = "todo"
	if _, err = s.SaveReport(ctx, audit.ID, "json", heads, []byte(`{"public":true}`), "fabricated-zero"); !errors.Is(err, ErrInputInvalid) {
		t.Fatal("zero review version cannot claim a plan")
	}
}

func TestAuditRejectsForgedEvidenceAndAssurance(t *testing.T) {
	for _, mode := range []string{"path", "subject", "policy", "assurance", "missing-assumptions", "verdict", "service", "observed-at"} {
		t.Run(mode, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "audit.sqlite"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			in, result := storedCase()
			snapshot, err := s.SaveSnapshot(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "path":
				result.Evidence[0].Field = "/policies/0"
			case "subject":
				result.Evidence[0].SubjectRef = "other-asset"
			case "policy":
				result.Evidence[0].PolicyRef = "missing-policy"
			case "assurance":
				result.Findings[0].AssessmentBasis = "live_observed"
			case "missing-assumptions":
				result.Findings[0].Assumptions = nil
			case "verdict":
				result.Findings[0].Verdict = "safe"
			case "service":
				result.Findings[0].ServiceRef = "not-owned-service"
			case "observed-at":
				result.Evidence[0].ObservedAt = "2027-01-01T00:00:00Z"
			}
			if _, err = s.SaveAudit(context.Background(), snapshot.ID, result); !errors.Is(err, ErrInputInvalid) {
				t.Fatalf("forged %s accepted: %v", mode, err)
			}
			runs, err := s.ListAudits(context.Background(), 0, 100)
			if err != nil || len(runs) != 0 {
				t.Fatal("invalid audit partially persisted")
			}
		})
	}
}

func TestStartupRequiresExplicitBusinessMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Exec("DROP TABLE business_schema").Error; err != nil {
		t.Fatal(err)
	}
	s.Close()
	if old, err := Open(path, false); err == nil {
		old.Close()
		t.Fatal("startup silently accepted an old schema")
	}
	s, err = Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestReviewHeadFailureRollsBackActionAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "head-failure.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in, result := storedCase()
	snapshot, err := s.SaveSnapshot(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.SaveAudit(ctx, snapshot.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	finding := run.Findings[0].ID
	if err = s.db.Exec("CREATE TRIGGER fail_head BEFORE UPDATE ON review_heads BEGIN SELECT RAISE(ABORT,'synthetic head failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	request := ReviewRequest{ClientRequestID: "atomic-head-key", ExpectedRevision: 0, Kind: "plan_change", Plan: "todo"}
	if _, err = s.Review(ctx, finding, request); err == nil {
		t.Fatal("head failure reported saved")
	}
	if lenMustHistory(t, s, finding) != 0 {
		t.Fatal("action committed despite head rollback")
	}
	_, heads, err := s.CaptureReportInput(ctx, run.ID)
	if err != nil || heads[0].Revision != 0 || heads[0].Plan != "" {
		t.Fatal("head partially committed")
	}
	if err = s.db.Exec("DROP TRIGGER fail_head").Error; err != nil {
		t.Fatal(err)
	}
	if saved, err := s.Review(ctx, finding, request); err != nil || saved.Revision != 1 {
		t.Fatal("failed action consumed idempotency key")
	}
}

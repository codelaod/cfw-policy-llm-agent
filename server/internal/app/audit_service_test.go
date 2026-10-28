package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

func serviceCase() domain.Input {
	return domain.Input{SchemaVersion: 1, SnapshotVersion: "untrusted-client-version", ProfileVersion: "profile-1", IntentVersion: "intent-1", NormalizerVersion: "1", KernelVersion: "1", SourceKind: "live_readonly", CollectedAt: "2026-10-27T23:59:00Z", EvaluationAt: "2026-10-28T00:00:00Z", Scope: domain.Scope{Ref: "scope-in", ControlPlaneRef: "control-demo", IPFamily: "ipv4", Provider: "aliyun", AccountRef: "private-account", Boundary: "internet", Direction: "in", PolicySetRef: "private-policy-set"}, Semantics: domain.Semantics{ProfileID: "aliyun-cfw-internet-v1", Version: "1", DefaultAction: "accept", SourceRef: "T-06"}, Completeness: domain.Completeness{Declared: true, EnumerationComplete: true, ObservationConsistency: "fixed_artifact"}, Protection: domain.Protection{Applicable: true, Required: true, State: "on", EvidenceRef: "protection-1"}, Selection: domain.Selection{Kind: "assets", AssetIDs: []string{"private-asset"}}, Assets: []domain.Asset{{ID: "private-asset", ScopeRef: "scope-in", Address: "192.0.2.10", AddressObserved: true, Services: []domain.Service{{ID: "ssh", Protocol: "tcp", Ports: []domain.PortRange{{From: 22, To: 22}}, Role: "management", RoleStatus: "confirmed", PublicAccess: "unknown"}}}}, AddressRefs: map[string]domain.AddressReference{}, PortRefs: map[string]domain.PortReference{}, Baseline: DefaultBaseline()}
}

func TestOfflineImportRulesReviewExportReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "offline.sqlite")
	store, err := storage.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	service := NewAuditService(store)
	in := serviceCase()
	encoded, _ := json.Marshal(in)
	snapshot, err := service.Import(ctx, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SourceKind != "imported" || snapshot.Input.SnapshotVersion == in.SnapshotVersion {
		t.Fatal("uploaded assurance or identity was trusted")
	}
	run, err := service.RunRules(ctx, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	var baseline, business storage.FindingRecord
	for _, item := range run.Findings {
		if item.Finding.RuleID == "CHK-07" && item.Finding.Verdict == "fail" {
			baseline = item
		}
		if item.Finding.RuleID == "CHK-01" {
			business = item
		}
	}
	if baseline.ID == "" || business.Finding.Verdict != "needs_confirmation" || baseline.Finding.AssessmentBasis != "imported_assumption" {
		t.Fatal("missing whitelist erased independent baseline or source assurance")
	}
	if _, err = service.Review(ctx, baseline.ID, storage.ReviewRequest{ClientRequestID: "review", Kind: "plan_change", Plan: "todo"}); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "markdown"} {
		report, err := service.Report(ctx, run.ID, format, "export-"+format)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"private-asset", "private-account", "private-policy-set", "192.0.2.10"} {
			if bytes.Contains(report.Content, []byte(private)) {
				t.Fatalf("public report leaked %s", private)
			}
		}
		if len(report.Content) == 0 || len(report.Reviews) != len(run.Findings) {
			t.Fatal("report missing fixed review snapshot")
		}
		if _, err = service.Review(ctx, baseline.ID, storage.ReviewRequest{ClientRequestID: "later-" + format, ExpectedRevision: int64(1 + map[string]int{"json": 0, "markdown": 1}[format]), Kind: "comment", Comment: "private free text"}); err != nil {
			t.Fatal(err)
		}
		replay, err := service.Report(ctx, run.ID, format, "export-"+format)
		if err != nil || !bytes.Equal(replay.Content, report.Content) {
			t.Fatal("old report changed after review")
		}
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = storage.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	service = NewAuditService(store)
	loaded, err := service.Store.GetAudit(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range loaded.Findings {
		if item.ID == baseline.ID && item.Finding.Verdict != "fail" {
			t.Fatal("review overwrote original rule")
		}
	}
}

func TestImportCannotChangeAdoptedBaselineOrRunCancelledWork(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "offline.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewAuditService(store)
	in := serviceCase()
	in.Baseline.RiskPorts = nil
	encoded, _ := json.Marshal(in)
	if _, err = service.Import(context.Background(), encoded); err == nil {
		t.Fatal("upload silently weakened adopted baseline")
	}
	in = serviceCase()
	encoded, _ = json.Marshal(in)
	snapshot, err := service.Import(context.Background(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = service.RunRules(ctx, snapshot.ID); err == nil {
		t.Fatal("cancelled operation completed")
	}
}

func TestImportBaselineComparisonUsesPolicyContent(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "baseline.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	in := serviceCase()
	in.Baseline.Exceptions = []domain.Exception{}
	for i, j := 0, len(in.Baseline.RiskPorts)-1; i < j; i, j = i+1, j-1 {
		in.Baseline.RiskPorts[i], in.Baseline.RiskPorts[j] = in.Baseline.RiskPorts[j], in.Baseline.RiskPorts[i]
	}
	for i, j := 0, len(in.Baseline.EnabledChecks)-1; i < j; i, j = i+1, j-1 {
		in.Baseline.EnabledChecks[i], in.Baseline.EnabledChecks[j] = in.Baseline.EnabledChecks[j], in.Baseline.EnabledChecks[i]
	}
	data, _ := json.Marshal(in)
	snapshot, err := NewAuditService(store).Import(context.Background(), data)
	if err != nil {
		t.Fatalf("same adopted baseline reordered or empty exceptions rejected: %v", err)
	}
	if snapshot.Input.Baseline.EnabledChecks[0] != "CHK-01" {
		t.Fatal("server must store canonical adopted baseline")
	}
}

func TestBuiltinExamplesUseRealRulesAndRemainSynthetic(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "examples.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewAuditService(store)
	for _, name := range []string{"internet-in", "internet-out", "nat-out"} {
		t.Run(name, func(t *testing.T) {
			snapshot, err := service.LoadExample(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.SourceKind != "synthetic" {
				t.Fatal("builtin source not server assigned")
			}
			run, err := service.RunRules(context.Background(), snapshot.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(run.Findings) == 0 || len(run.Result.Evidence) == 0 {
				t.Fatal("example did not produce real persisted rule output")
			}
			if _, err = service.Report(context.Background(), run.ID, "json", "example-report-"+name); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err = service.LoadExample(context.Background(), "../../.env.local"); err == nil {
		t.Fatal("unknown example must never become a file path")
	}
}

func TestInterruptedRuleStageRetainsCommittedPartialResults(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "partial.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewAuditService(store)
	data, _ := json.Marshal(serviceCase())
	snapshot, err := service.Import(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.rules = func(ctx context.Context, in domain.Input) (domain.Result, error) {
		result, err := audit.Run(ctx, in)
		if err != nil {
			return result, err
		}
		result.Coverage.Complete = false
		result.Coverage.Limitations = append(result.Coverage.Limitations, "RULE_STAGE_INTERRUPTED")
		cancel()
		return result, context.Canceled
	}
	partial, err := service.RunRules(ctx, snapshot.ID)
	if !errors.Is(err, context.Canceled) || partial.ID == "" || partial.Result.Coverage.Complete || len(partial.Findings) == 0 {
		t.Fatalf("partial results discarded or marked successful: id=%s error=%v", partial.ID, err)
	}
	loaded, err := store.GetAudit(context.Background(), partial.ID)
	if err != nil || loaded.Result.Coverage.Complete || len(loaded.Findings) == 0 {
		t.Fatal("partial snapshot not durable")
	}
}

func TestRepeatedRequiredDeclarationsRemainPersistable(t *testing.T) {
	for _, mode := range []string{"two-unknown", "duplicate-applicable"} {
		t.Run(mode, func(t *testing.T) {
			store, err := storage.Open(filepath.Join(t.TempDir(), "required.sqlite"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service := NewAuditService(store)
			in := serviceCase()
			need := domain.Access{ScopeRef: in.Scope.Ref, Protocol: "tcp", Addresses: []string{"198.51.100.0/24"}, Ports: []domain.PortRange{{From: 22, To: 22}}, Applicability: "unknown"}
			if mode == "duplicate-applicable" {
				need.Applicability = "applicable"
			}
			in.Assets[0].Services[0].RequiredConfirmed = true
			in.Assets[0].Services[0].Required = []domain.Access{need, need}
			if mode == "two-unknown" {
				in.Assets[0].Services[0].Required[1].Addresses = []string{"203.0.113.0/24"}
			}
			data, _ := json.Marshal(in)
			snapshot, err := service.Import(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			run, err := service.RunRules(context.Background(), snapshot.ID)
			if err != nil || run.ID == "" {
				t.Fatalf("legal repeated declarations could not persist: %v", err)
			}
		})
	}
}

func TestCancellationWhileSavingKeepsComputedFacts(t *testing.T){
	store,err:=storage.Open(filepath.Join(t.TempDir(),"save-cancel.sqlite"),true);if err!=nil{t.Fatal(err)};defer store.Close();service:=NewAuditService(store)
	data,_:=json.Marshal(serviceCase());snapshot,err:=service.Import(context.Background(),data);if err!=nil{t.Fatal(err)}
	writer,err:=store.SQL.Conn(context.Background());if err!=nil{t.Fatal(err)};defer writer.Close()
	if _,err=writer.ExecContext(context.Background(),"BEGIN IMMEDIATE");err!=nil{t.Fatal(err)};defer writer.ExecContext(context.Background(),"ROLLBACK")
	computed:=make(chan struct{})
	service.rules=func(ctx context.Context,in domain.Input)(domain.Result,error){result,err:=audit.Run(ctx,in);close(computed);return result,err}
	ctx,cancel:=context.WithCancel(context.Background());defer cancel()
	type outcome struct{record storage.AuditRecord;err error};done:=make(chan outcome,1)
	go func(){record,err:=service.RunRules(ctx,snapshot.ID);done<-outcome{record,err}}()
	select{case <-computed:case <-time.After(time.Second):t.Fatal("rules did not finish independently of writer")}
	deadline:=time.Now().Add(time.Second)
	for store.SQL.Stats().InUse<2{if time.Now().After(deadline){t.Fatal("save did not enter SQLite wait")};time.Sleep(time.Millisecond)}
	cancel()
	if _,err=writer.ExecContext(context.Background(),"COMMIT");err!=nil{t.Fatal(err)}
	select{case value:=<-done:if !errors.Is(value.err,context.Canceled)||value.record.ID==""{t.Fatalf("computed facts lost or cancelled operation returned success: id=%s err=%v",value.record.ID,value.err)};case <-time.After(2*time.Second):t.Fatal("save did not finish after write lock released")}
}

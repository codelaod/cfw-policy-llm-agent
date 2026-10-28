package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/audit"
	"cfw-policy-llm-agent/server/internal/domain"
)

func cloudCase(t *testing.T, kind string) (*Store, Job, domain.Input, CloudContent) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cloud.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	in, err := audit.SyntheticExample("internet-in")
	if err != nil {
		t.Fatal(err)
	}
	in.SourceKind = "live_readonly"
	in.SnapshotVersion = "live-fixture-v1"
	in.Completeness.ObservationConsistency = "observed_stable"
	if err = s.RegisterConfiguredScopes(context.Background(), "aliyun-explicit", "cloud-config-v1", []domain.Scope{in.Scope}); err != nil {
		t.Fatal(err)
	}
	conversation, err := s.CreateConversation(context.Background(), "采集人工fixture", in.Scope.Ref)
	if err != nil {
		t.Fatal(err)
	}
	prepared := domain.Input{Scope: in.Scope, Selection: in.Selection, Baseline: in.Baseline}
	p, err := s.SavePlan(context.Background(), ExecutionPlan{Kind: kind, ConversationID: conversation.ID, ConversationRevision: conversation.Revision, DataSourceID: "aliyun-explicit", PolicyScopeRef: in.Scope.Ref, Mode: kind, ConfigRevision: "cloud-config-v1", BudgetRef: "cloud-budget", PreparedInput: prepared, EffectCeiling: EffectCeiling{AllowCloudReads: true, CloudHTTPAttempts: 6}})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := s.EnqueuePlan(context.Background(), p.ID, "cloud-operation", p.ConfigRevision)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := s.Claim(context.Background())
	if err != nil || !found {
		t.Fatal("claim cloud job", err)
	}
	observations := []CloudObservation{{Round: 1, Operation: "DescribeControlPolicy", Pages: 1, StartedAt: in.CollectedAt, FinishedAt: in.CollectedAt, EnumerationComplete: true, SemanticSHA256: strings.Repeat("a", 64)}}
	rawBody := json.RawMessage(`{"synthetic_private_raw":"do-not-serialize"}`)
	hash := sha256.Sum256(rawBody)
	rawArtifacts, _ := json.Marshal([]CloudRawArtifact{{Round: 1, Operation: "DescribeControlPolicy", Query: map[string]string{"Direction": "in"}, Body: []byte(rawBody), SHA256: hex.EncodeToString(hash[:])}})
	content := CloudContent{JobID: job.ID, Kind: kind, ScopeRef: in.Scope.Ref, SourceRef: "aliyun-explicit", ConfigRevision: "cloud-config-v1", IdentityVerified: true, ProductReady: true, Observations: observations, Limitations: []string{}, RawArtifacts: rawArtifacts}
	return s, job, in, content
}

func TestE4FinalizeCollectAtomicallySealsNewSnapshotAndPublicReply(t *testing.T) {
	s, job, in, content := cloudCase(t, "collect")
	record, err := s.FinalizeCloud(context.Background(), job.ID, content, &in)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.ReadJobWorkspace(context.Background(), job.ID)
	if err != nil || view.Job.Status != "completed" || view.CloudResult == nil || view.SnapshotID == "" || view.SnapshotID != record.Content.SnapshotID || !view.InputSealed {
		t.Fatal("cloud products not committed", view, err)
	}
	sealed, err := s.GetJobInput(context.Background(), job.ID)
	if err != nil || !reflect.DeepEqual(sealed, in) {
		t.Fatal("sealed cloud input differs", err)
	}
	snapshot, err := s.GetSnapshot(context.Background(), record.Content.SnapshotID)
	if err != nil || !reflect.DeepEqual(snapshot.Input, in) {
		t.Fatal("snapshot differs", err)
	}
	plan, err := s.GetPlan(context.Background(), job.PlanID)
	if err != nil || plan.SnapshotID != "" || plan.PreparedInput.SchemaVersion != 0 {
		t.Fatal("finalization rewrote accepted intent", err)
	}
	encoded, err := json.Marshal(view.CloudResult)
	if err != nil || strings.Contains(string(encoded), "do-not-serialize") || strings.Contains(string(encoded), "raw_artifacts") || strings.Contains(string(encoded), "cloud-config-v1") {
		t.Fatal("private cloud artifacts entered public result")
	}
	page, _, err := s.Page(context.Background(), job.ID, 0, 100)
	if err != nil || page.Events[len(page.Events)-1].Type != "job_terminal" {
		t.Fatal("terminal event missing", err)
	}
	messages, err := s.ConversationMessages(context.Background(), job.ConversationID, 0, 100)
	if err != nil || len(messages.Items) != 2 || messages.Items[1].Origin != "application" {
		t.Fatal("fixed public reply missing", err)
	}
	for _, message := range messages.Items {
		if strings.Contains(message.PublicText, "do-not-serialize") {
			t.Fatal("raw in message")
		}
	}
	if _, err = s.FinalizeCloud(context.Background(), job.ID, content, &in); err != nil {
		t.Fatal("identical finalization not idempotent", err)
	}
	content.Limitations = []string{"IDENTITY_ONLY"}
	if _, err = s.FinalizeCloud(context.Background(), job.ID, content, &in); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
}

func TestE4FinalizeCloudRejectsWrongBindingsAndUntrustedMetadata(t *testing.T) {
	for _, scenario := range []string{"job", "kind", "source", "revision", "scope", "selection", "baseline", "source-kind", "unverified", "caller-snapshot", "bad-observation", "raw-identity-observation", "raw-identity", "raw-credential", "free-limitation", "cancelled", "cancelled-context"} {
		t.Run(scenario, func(t *testing.T) {
			s, job, in, content := cloudCase(t, "collect")
			ctx := context.Background()
			switch scenario {
			case "job":
				content.JobID = "other-job"
			case "kind":
				content.Kind = "connection_check"
			case "source":
				content.SourceRef = "other-source"
			case "revision":
				content.ConfigRevision = "other-config"
			case "scope":
				in.Scope.AccountRef = "other-account"
			case "selection":
				in.Selection.AssetIDs = []string{"other-asset"}
			case "baseline":
				in.Baseline.Version = "other-baseline"
			case "source-kind":
				in.SourceKind = "synthetic"
			case "unverified":
				content.IdentityVerified = false
			case "caller-snapshot":
				content.SnapshotID = "snapshot-caller-selected"
			case "bad-observation":
				content.Observations[0].Operation = "DeleteControlPolicy"
			case "raw-identity-observation":
				content.Observations[0].SemanticSHA256 = "private-account-id"
			case "raw-identity":
				content.RawArtifacts = json.RawMessage(`[{"round":1,"operation":"GetCallerIdentity","body":{"AccountId":"123456"}}]`)
			case "raw-credential":
				content.RawArtifacts = json.RawMessage(`[{"round":1,"operation":"DescribeControlPolicy","body":{"AccessKeySecret":"synthetic-only"}}]`)
			case "free-limitation":
				content.Limitations = []string{"private endpoint response"}
			case "cancelled":
				if _, err := s.Cancel(ctx, job.ID); err != nil {
					t.Fatal(err)
				}
			case "cancelled-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := s.FinalizeCloud(ctx, job.ID, content, &in); err == nil {
				t.Fatal("invalid finalization accepted")
			}
			var count int64
			if err := s.db.Raw("SELECT count(*) FROM snapshots").Scan(&count).Error; err != nil || count != 0 {
				t.Fatal("invalid finalization left snapshot", err)
			}
			view, err := s.ReadJobWorkspace(context.Background(), job.ID)
			if err != nil || view.CloudResult != nil || view.InputSealed {
				t.Fatal("invalid finalization partially saved", err)
			}
		})
	}
}

func TestE4FinalizeCloudFailureRollsBackAllProducts(t *testing.T) {
	for _, table := range []string{"snapshots", "job_cloud_results", "conversation_messages", "e0_events"} {
		t.Run(table, func(t *testing.T) {
			s, job, in, content := cloudCase(t, "collect")
			if err := s.db.Exec("CREATE TRIGGER cloud_fault BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'synthetic cloud fault'); END").Error; err != nil {
				t.Fatal(err)
			}
			if _, err := s.FinalizeCloud(context.Background(), job.ID, content, &in); err == nil {
				t.Fatal("injected failure reported success")
			}
			var count int64
			if err := s.db.Raw("SELECT count(*) FROM snapshots").Scan(&count).Error; err != nil || count != 0 {
				t.Fatal("snapshot committed without products", err)
			}
			view, err := s.ReadJobWorkspace(context.Background(), job.ID)
			if err != nil || view.CloudResult != nil || view.InputSealed || view.Job.Status != "running" {
				t.Fatal("finalization partially committed", err)
			}
		})
	}
}

func TestE4CloudConnectionIdentityOnlyRemainsPartialAndHasNoInput(t *testing.T) {
	s, job, in, content := cloudCase(t, "connection_check")
	content.ProductReady = false
	content.Limitations = []string{"IDENTITY_ONLY"}
	content.Observations = []CloudObservation{}
	content.RawArtifacts = json.RawMessage(`[]`)
	if _, err := s.FinalizeCloud(context.Background(), job.ID, content, &in); err == nil {
		t.Fatal("connection check accepted input")
	}
	if _, err := s.FinalizeCloud(context.Background(), job.ID, content, nil); err != nil {
		t.Fatal(err)
	}
	view, err := s.ReadJobWorkspace(context.Background(), job.ID)
	if err != nil || view.Job.Status != "partial" || view.InputSealed || view.SnapshotID != "" || view.CloudResult == nil || view.CloudResult.Content.ProductReady {
		t.Fatal("identity-only check overclaimed readiness", view, err)
	}
}

func TestE4CloudCannotCompleteWithoutAtomicProduct(t *testing.T) {
	for _, kind := range []string{"collect", "connection_check"} {
		t.Run(kind, func(t *testing.T) {
			s, job, in, _ := cloudCase(t, kind)
			if _, err := s.SealInput(context.Background(), job.ID, domain.Input{Scope: in.Scope, Selection: in.Selection, Baseline: in.Baseline}); err == nil {
				t.Error("legacy sealing accepted a cloud collection intent as evidence")
			}
			if err := s.FinishWithCode(context.Background(), job.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			view, err := s.ReadJobWorkspace(context.Background(), job.ID)
			if err != nil || view.Job.Status != "failed" || view.Job.ErrorCode != "CLOUD_RESULT_NOT_SAVED" || view.CloudResult != nil {
				t.Fatal("empty executor claimed completed cloud work", view, err)
			}
		})
	}
}

func TestE4CloudIncompleteInputCannotOverclaimCompleted(t *testing.T) {
	s, job, in, content := cloudCase(t, "collect")
	in.Completeness.EnumerationComplete = false
	in.Completeness.KnownGaps = []string{"MISSING_POLICY_PAGE"}
	if _, err := s.FinalizeCloud(context.Background(), job.ID, content, &in); err != nil {
		t.Fatal(err)
	}
	view, err := s.ReadJobWorkspace(context.Background(), job.ID)
	if err != nil || view.Job.Status != "partial" || view.CloudResult == nil || len(view.CloudResult.Content.Limitations) == 0 {
		t.Fatal("incomplete collection became complete", err)
	}
}

func TestE4PrivateCloudArtifactsPreserveExactWireAndRejectAmbiguousEnvelopes(t *testing.T) {
	for _, scenario := range []string{"pretty", "duplicate-envelope", "duplicate-wire", "credential", "hash"} {
		t.Run(scenario, func(t *testing.T) {
			s, job, in, content := cloudCase(t, "collect")
			wire := []byte("{\n  \"synthetic_private_raw\": \"exact wire\"\n}\n")
			if scenario == "duplicate-wire" {
				wire = []byte(`{"a":1,"a":2}`)
			}
			if scenario == "credential" {
				wire = []byte(`{"nested":{"SecurityToken":"synthetic-private-token"}}`)
			}
			hash := sha256.Sum256(wire)
			artifacts := []CloudRawArtifact{{Round: 1, Operation: "DescribeControlPolicy", Query: map[string]string{"Direction": "in"}, Body: wire, SHA256: hex.EncodeToString(hash[:])}}
			if scenario == "hash" {
				artifacts[0].SHA256 = strings.Repeat("a", 64)
			}
			content.RawArtifacts, _ = json.Marshal(artifacts)
			if scenario == "duplicate-envelope" {
				content.RawArtifacts = json.RawMessage(strings.Replace(string(content.RawArtifacts), `"operation":"DescribeControlPolicy"`, `"operation":"GetCallerIdentity","operation":"DescribeControlPolicy"`, 1))
			}
			_, err := s.FinalizeCloud(context.Background(), job.ID, content, &in)
			if scenario != "pretty" {
				if err == nil {
					t.Fatal("untrusted raw artifact accepted")
				}
				return
			}
			if err != nil {
				t.Fatal("exact wire artifact rejected", err)
			}
			var raw string
			if err = s.db.Raw("SELECT raw_artifacts FROM job_cloud_results WHERE job_id=?", job.ID).Scan(&raw).Error; err != nil {
				t.Fatal(err)
			}
			var saved []CloudRawArtifact
			if json.Unmarshal([]byte(raw), &saved) != nil || len(saved) != 1 || string(saved[0].Body) != string(wire) {
				t.Fatal("provider wire bytes changed in persistence")
			}
		})
	}
}

func TestE4AcceptedCloudCompletionSurvivesScopeChangeAndArchive(t *testing.T) {
	for _, kind := range []string{"collect", "connection_check"} {
		t.Run(kind, func(t *testing.T) {
			s, job, in, content := cloudCase(t, kind)
			conversation, err := s.GetConversation(context.Background(), job.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			archived := true
			if _, err = s.PatchConversation(context.Background(), conversation.ID, ConversationPatch{ExpectedRevision: conversation.Revision, PolicyScopeRef: ptrString(""), Archived: &archived}); err != nil {
				t.Fatal(err)
			}
			if err = s.RegisterConfiguredScopes(context.Background(), content.SourceRef, "cloud-config-v2", nil); err != nil {
				t.Fatal(err)
			}
			var input *domain.Input = &in
			if kind == "connection_check" {
				input = nil
				content.ProductReady = false
				content.Limitations = []string{"IDENTITY_ONLY"}
				content.Observations = []CloudObservation{}
				content.RawArtifacts = json.RawMessage(`[]`)
			}
			if _, err = s.FinalizeCloud(context.Background(), job.ID, content, input); err != nil {
				t.Fatal("accepted task lost original completion context", err)
			}
		})
	}
}

func ptrString(value string) *string { return &value }

func TestE4CloudCancellationWhileWaitingForDatabaseCannotPublish(t *testing.T) {
	s, job, in, content := cloudCase(t, "collect")
	s.SQL.SetMaxOpenConns(1)
	held, err := s.SQL.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	waits := s.SQL.Stats().WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := s.FinalizeCloud(ctx, job.ID, content, &in); done <- e }()
	until := time.Now().Add(time.Second)
	for s.SQL.Stats().WaitCount == waits {
		if time.Now().After(until) {
			t.Fatal("finalization never waited for connection")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	held.Close()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled finalization lost context", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled finalization did not stop")
	}
	view, err := s.ReadJobWorkspace(context.Background(), job.ID)
	if err != nil || view.InputSealed || view.CloudResult != nil || view.Job.Status != "running" {
		t.Fatal("late cloud completion committed", err)
	}
}

func TestE4CloudRestartKeepsProductsAndNeverReplaysUnfinishedCollection(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted", true: "completed"}[complete], func(t *testing.T) {
			s, job, in, content := cloudCase(t, "collect")
			var saved CloudRecord
			var err error
			if complete {
				saved, err = s.FinalizeCloud(context.Background(), job.ID, content, &in)
				if err != nil {
					t.Fatal(err)
				}
			}
			var file struct{ File string }
			if err = s.db.Raw("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&file).Error; err != nil {
				t.Fatal(err)
			}
			s.Close()
			reopened, err := Open(file.File, false)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err = reopened.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			view, err := reopened.ReadJobWorkspace(context.Background(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if complete {
				if view.Job.Status != "completed" || view.CloudResult == nil || view.CloudResult.ID != saved.ID || view.CloudResult.Content.RawArtifacts != nil || view.SnapshotID != saved.Content.SnapshotID {
					t.Fatal("committed cloud product lost on restart")
				}
			} else {
				if view.Job.Status != "interrupted" || view.CloudResult != nil || view.InputSealed {
					t.Fatal("restart manufactured collection products")
				}
				if _, err = reopened.FinalizeCloud(context.Background(), job.ID, content, &in); !errors.Is(err, ErrJobTerminal) {
					t.Fatal("interrupted task accepted late collection", err)
				}
			}
		})
	}
}

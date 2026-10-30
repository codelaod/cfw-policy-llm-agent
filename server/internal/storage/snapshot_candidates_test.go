package storage

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSnapshotCandidatesReadCommittedCloudBindingWithoutPrivateRaw(t *testing.T) {
	s, job, in, content := cloudCase(t, "collect")
	before, err := s.SnapshotCandidates(context.Background(), SnapshotCandidateQuery{ScopeRef: in.Scope.Ref, Limit: 10})
	if err != nil || len(before) != 0 {
		t.Fatal("inflight input became a cache candidate", err)
	}
	cloud, err := s.FinalizeCloud(context.Background(), job.ID, content, &in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SnapshotCandidates(context.Background(), SnapshotCandidateQuery{ScopeRef: in.Scope.Ref, Limit: 10})
	if err != nil || len(got) != 1 {
		t.Fatal("committed snapshot unavailable", err)
	}
	c := got[0]
	if c.Snapshot.ID != cloud.Content.SnapshotID || c.Cloud == nil || c.Cloud.SourceRef != "aliyun-explicit" || c.Cloud.ConfigRevision != "cloud-config-v1" || c.ProducerStatus != "completed" || len(c.Cloud.Content.RawArtifacts) != 0 {
		t.Fatal("wrong producer binding or raw artifact exposure")
	}
	encoded, _ := json.Marshal(c)
	if string(encoded) != "{}" {
		t.Fatal("candidate accidentally serialized its full input")
	}
	after, err := s.GetSnapshot(context.Background(), c.Snapshot.ID)
	if err != nil || after.Input.CollectedAt != in.CollectedAt || after.Input.SnapshotVersion != in.SnapshotVersion {
		t.Fatal("candidate lookup rewrote input time or manifest", err)
	}
}
func TestSnapshotCandidatesKeepOfflineAndUnboundLiveDistinct(t *testing.T) {
	s, _, in, _ := cloudCase(t, "collect")
	in.SourceKind = "imported"
	offline, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.SourceKind = "live_readonly"
	unbound, err := s.SaveSnapshot(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{offline.ID, unbound.ID} {
		got, err := s.SnapshotCandidates(context.Background(), SnapshotCandidateQuery{ScopeRef: in.Scope.Ref, SnapshotID: id, Limit: 1})
		if err != nil || len(got) != 1 || got[0].Cloud != nil || got[0].ProducerStatus != "" {
			t.Fatal("unbound or offline data was promoted to collected cloud", err)
		}
	}
	if got, err := s.SnapshotCandidates(context.Background(), SnapshotCandidateQuery{ScopeRef: "outside-scope", Limit: 10}); err != nil || len(got) != 0 {
		t.Fatal("query crossed scope", err)
	}
	if _, err := s.SnapshotCandidates(context.Background(), SnapshotCandidateQuery{ScopeRef: in.Scope.Ref, Limit: 0}); err == nil {
		t.Fatal("unbounded candidate search accepted")
	}
}

func TestSnapshotCandidatesSeparatePreparedDataFromParentOutcome(t *testing.T) {
	for _, state := range []string{"running", "partial", "failed", "cancelled", "interrupted"} {
		t.Run(state, func(t *testing.T) {
			s, p, in, content := preparationCase(t, "internet-in", false)
			_, producer, content := acceptPreparation(t, s, p, content)
			ctx := context.Background()
			record, err := s.SavePreparedCloudSnapshot(ctx, producer.ID, content, in)
			if err != nil {
				t.Fatal(err)
			}
			if state != "running" {
				// The storage query must interpret all persisted terminal states,
				// including cancellation and recovery, independently of data status.
				if err = s.db.Exec("UPDATE e0_jobs SET status=? WHERE id=?", state, producer.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			candidates, err := s.SnapshotCandidates(ctx, SnapshotCandidateQuery{ScopeRef: in.Scope.Ref, SnapshotID: record.Content.SnapshotID, Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			if state == "running" {
				if len(candidates) != 0 {
					t.Fatal("running audit product became reusable candidate")
				}
				return
			}
			if len(candidates) != 1 || candidates[0].Cloud == nil || candidates[0].Cloud.Content.Kind != "audit" || candidates[0].DataStatus != "complete" || candidates[0].ProducerStatus != state {
				t.Fatal("prepared product lost actual producer or data status", candidates)
			}
		})
	}
}

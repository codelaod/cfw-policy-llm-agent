package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"cfw-policy-llm-agent/server/internal/audit"
)

func TestIndependentRebaseCurrentBaseIsReadOnlyAndDeclaredFieldsStayIndependent(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	in, e := audit.SyntheticExample("internet-in")
	if e != nil {
		t.Fatal(e)
	}
	in.Assets[0].ID = "vm.web.0"
	in.Selection.AssetIDs = []string{"vm.web.0", "vm.web.1"}
	clone, _ := json.Marshal(in.Assets[0])
	second := in.Assets[0]
	_ = json.Unmarshal(clone, &second)
	second.ID, second.Address = "vm.web.1", "192.0.2.21"
	in.Assets = append(in.Assets, second)
	snapshot, e := s.SaveSnapshot(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	draft := mustDraft(t, s, snapshot, in.Selection.AssetIDs...)
	before, e := s.GetProfileDraft(ctx, draft.ID)
	if e != nil {
		t.Fatal(e)
	}
	base, e := s.ProfileDraftCurrentBase(ctx, draft.ID)
	readAgain, readErr := s.GetProfileDraft(ctx, draft.ID)
	if e != nil || readErr != nil || !reflect.DeepEqual(before, readAgain) {
		t.Fatal("reading current base edited a saved draft", e, readErr)
	}
	base.Changes[0].Roles[0].Role = "database"
	*base.Changes[0].Roles[0].Declared = true
	if *base.Changes[0].Roles[1].Declared || *base.Changes[1].Roles[0].Declared || *base.Changes[0].Access.AllowedDeclared || *base.Changes[0].Access.Services[0].AllowedDeclared || *base.Changes[0].Access.Services[0].RequiredDeclared {
		t.Fatal("selecting one role aliased unrelated declaration fields")
	}
	updated, e := s.RebaseProfileDraft(ctx, draft.ID, RebaseDraftRequest{ExpectedDraftRevision: draft.Revision, ExpectedBaseRevisions: base.BaseRevisions, Changes: base.Changes}, "independent-selected-role-rebase")
	if e != nil {
		t.Fatal(e)
	}
	preview := mustPreview(t, s, updated.ID)
	if !reflect.DeepEqual(preview.ConfirmableSections, []string{"roles"}) {
		t.Fatal("rebasing one selected role enabled access confirmation", preview.ConfirmableSections)
	}
	mustConfirm(t, s, updated, preview, "independent-selected-role-confirm", "roles")
	current, heads, e := s.FreezeInput(ctx, snapshot.ID)
	if e != nil || current.Assets[0].Services[0].Role != "database" || current.Assets[1].Services[0].Role != in.Assets[1].Services[0].Role {
		t.Fatal("selected role changed another asset", e)
	}
	for _, head := range heads {
		if head.IntentVersionID != "" || head.AssetID == "vm.web.1" && (head.Revision != 0 || head.ProfileVersionID != "") {
			t.Fatal("quiet declarations changed unrelated heads", head)
		}
	}
}

func TestIndependentRebaseRejectsActualHeadAdvanceAfterReview(t *testing.T) {
	s := collaborationStore(t)
	ctx := context.Background()
	snapshot := collaborationSnapshot(t, s)
	asset := snapshot.Input.Assets[0].ID
	draft := mustDraft(t, s, snapshot, asset)
	base, e := s.ProfileDraftCurrentBase(ctx, draft.ID)
	if e != nil {
		t.Fatal(e)
	}
	other := mustDraft(t, s, snapshot, asset)
	other.Changes[0].Roles[0].Role = "management"
	other, e = s.UpdateProfileDraft(ctx, other.ID, UpdateDraftRequest{ExpectedDraftRevision: other.Revision, Changes: other.Changes})
	if e != nil {
		t.Fatal(e)
	}
	mustConfirm(t, s, other, mustPreview(t, s, other.ID), "independent-head-winner", "roles")
	_, e = s.RebaseProfileDraft(ctx, draft.ID, RebaseDraftRequest{ExpectedDraftRevision: draft.Revision, ExpectedBaseRevisions: base.BaseRevisions, Changes: base.Changes}, "independent-reviewed-old-head")
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("actual head change after GET was not compared", e)
	}
	current, e := s.GetProfileDraft(ctx, draft.ID)
	if e != nil || current.Revision != draft.Revision || !reflect.DeepEqual(current.Changes, draft.Changes) {
		t.Fatal("failed CAS changed the draft", e)
	}
}

func TestIndependentRebaseReceiptSurvivesRestartAndDoesNotRewindLaterEdit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rebase-replay.sqlite")
	s, e := Open(path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	snapshot := collaborationSnapshot(t, s)
	asset := snapshot.Input.Assets[0].ID
	draft := mustDraft(t, s, snapshot, asset)
	base, e := s.ProfileDraftCurrentBase(ctx, draft.ID)
	if e != nil {
		t.Fatal(e)
	}
	req := RebaseDraftRequest{ExpectedDraftRevision: draft.Revision, ExpectedBaseRevisions: base.BaseRevisions, Changes: base.Changes}
	saved, e := s.RebaseProfileDraft(ctx, draft.ID, req, "independent-durable-rebase")
	if e != nil {
		t.Fatal(e)
	}
	changes, _ := json.Marshal(saved.Changes)
	var edited []AssetDraftChange
	_ = json.Unmarshal(changes, &edited)
	edited[0].Roles[0].Role = "management"
	latest, e := s.UpdateProfileDraft(ctx, draft.ID, UpdateDraftRequest{ExpectedDraftRevision: saved.Revision, Changes: edited})
	if e != nil {
		t.Fatal(e)
	}
	other := mustDraft(t, s, snapshot, asset)
	mustConfirm(t, s, other, mustPreview(t, s, other.ID), "independent-post-receipt-head", "roles")
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s = nil
	s, e = Open(path, false)
	if e != nil {
		t.Fatal(e)
	}
	replayed, e := s.RebaseProfileDraft(ctx, draft.ID, req, "independent-durable-rebase")
	if e != nil || replayed.Revision != saved.Revision || !reflect.DeepEqual(replayed.Changes, saved.Changes) {
		t.Fatal("old receipt was subjected to newer heads/revision", e)
	}
	current, e := s.GetProfileDraft(ctx, draft.ID)
	if e != nil || current.Revision != latest.Revision || !reflect.DeepEqual(current.Changes, latest.Changes) {
		t.Fatal("replaying a receipt rewound a later edit", e)
	}
}

func TestIndependentRebaseTerminalDraftsRejectNewEdits(t *testing.T) {
	for _, status := range []string{"confirmed", "rejected"} {
		t.Run(status, func(t *testing.T) {
			s := collaborationStore(t)
			ctx := context.Background()
			snapshot := collaborationSnapshot(t, s)
			draft := mustDraft(t, s, snapshot, snapshot.Input.Assets[0].ID)
			if status == "confirmed" {
				mustConfirm(t, s, draft, mustPreview(t, s, draft.ID), "independent-terminal-confirm", "roles")
			} else if _, e := s.RejectProfileDraft(ctx, draft.ID, draft.Revision, "independent-terminal-reject"); e != nil {
				t.Fatal(e)
			}
			base, e := s.ProfileDraftCurrentBase(ctx, draft.ID)
			if e != nil {
				t.Fatal(e)
			}
			_, e = s.RebaseProfileDraft(ctx, draft.ID, RebaseDraftRequest{ExpectedDraftRevision: base.DraftRevision, ExpectedBaseRevisions: base.BaseRevisions, Changes: base.Changes}, "independent-terminal-rebase")
			if !errors.Is(e, ErrVersionConflict) {
				t.Fatal("terminal draft accepted a new rebase", status, e)
			}
		})
	}
}

func TestIndependentRebaseMissingReceiptStructureRequiresExplicitMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rebase-shape.sqlite")
	s, e := Open(path, true)
	if e != nil {
		t.Fatal(e)
	}
	snapshot := collaborationSnapshot(t, s)
	draft := mustDraft(t, s, snapshot, snapshot.Input.Assets[0].ID)
	if e = s.db.Exec("DROP TABLE profile_draft_rebase_requests").Error; e != nil {
		_ = s.Close()
		t.Fatal(e)
	}
	_ = s.Close()
	if unexpected, err := Open(path, false); err == nil {
		_ = unexpected.Close()
		t.Fatal("normal startup accepted missing rebase receipt structure")
	}
	s, e = Open(path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	current, e := s.GetProfileDraft(ctx, draft.ID)
	if e != nil || current.ID != draft.ID || current.Revision != draft.Revision || !reflect.DeepEqual(current.Changes, draft.Changes) {
		t.Fatal("explicit schema repair reset an existing draft", e)
	}
	if _, e = s.ProfileDraftCurrentBase(ctx, draft.ID); e != nil {
		t.Fatal("restored receipt schema did not retain current-base access", e)
	}
}

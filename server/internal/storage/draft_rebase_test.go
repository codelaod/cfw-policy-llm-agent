package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestSameDraftExplicitRebasePreservesMappingAndConfirmsNothing(t *testing.T) {
	s, job, snapshot, target := conversationDraftCase(t)
	ctx := context.Background()
	asset := snapshot.Input.Assets[0].ID
	local := mustDraft(t, s, snapshot, asset)
	if e := s.db.Exec("INSERT INTO conversation_business_drafts(conversation_id,target_ref,draft_id) VALUES(?,?,?)", job.ConversationID, target.Target.Ref, local.ID).Error; e != nil {
		t.Fatal(e)
	}
	oldPreview := mustPreview(t, s, local.ID)
	other := mustDraft(t, s, snapshot, asset)
	other.Changes[0].Roles[0].Role = "database"
	other, e := s.UpdateProfileDraft(ctx, other.ID, UpdateDraftRequest{ExpectedDraftRevision: other.Revision, Changes: other.Changes})
	if e != nil {
		t.Fatal(e)
	}
	mustConfirm(t, s, other, mustPreview(t, s, other.ID), "rebase-other-confirm", "roles")
	if _, e = s.PreviewProfileDraft(ctx, local.ID); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("fixture did not have changed confirmed heads", e)
	}
	base, e := s.ProfileDraftCurrentBase(ctx, local.ID)
	if e != nil || base.DraftID != local.ID || base.BaseRevisions[0].Revision != 1 || base.Changes[0].Roles[0].Role != "database" {
		t.Fatal("current base did not retain the same target", e)
	}
	if *base.Changes[0].Roles[0].Declared || *base.Changes[0].Access.AllowedDeclared || *base.Changes[0].Access.Services[0].RequiredDeclared {
		t.Fatal("reading current heads declared business sections")
	}
	base.Changes[0].Roles[0].Role = "web"
	*base.Changes[0].Roles[0].Declared = true
	if *base.Changes[0].Access.AllowedDeclared || *base.Changes[0].Access.Services[0].RequiredDeclared {
		t.Fatal("selecting one role silently selected access fields")
	}
	req := RebaseDraftRequest{ExpectedDraftRevision: local.Revision, ExpectedBaseRevisions: base.BaseRevisions, Changes: base.Changes}
	before, heads, e := s.FreezeInput(ctx, snapshot.ID)
	if e != nil {
		t.Fatal(e)
	}
	updated, e := s.RebaseProfileDraft(ctx, local.ID, req, "same-draft-rebase")
	if e != nil || updated.ID != local.ID || updated.Revision != local.Revision+1 || updated.Status != "draft" || updated.PreviewID != "" || updated.ConfirmationID != "" {
		t.Fatal("rebase replaced the draft or confirmed it", e)
	}
	after, newHeads, e := s.FreezeInput(ctx, snapshot.ID)
	if e != nil || !reflect.DeepEqual(heads, newHeads) || !reflect.DeepEqual(before, after) {
		t.Fatal("rebase changed confirmed business data", e)
	}
	context, e := s.ConversationBusinessContext(ctx, job.ConversationID)
	if e != nil || context.Draft == nil || context.Draft.ID != local.ID || context.Target.Ref != target.Target.Ref {
		t.Fatal("same-target conversation binding was lost", e)
	}
	if _, e = s.ConfirmProfileDraft(ctx, local.ID, ConfirmDraftRequest{PreviewID: oldPreview.ID, ExpectedDraftRevision: local.Revision, ConfirmedSections: []string{"roles"}}, "old-rebase-preview"); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("old preview survived rebase", e)
	}
	replayed, e := s.RebaseProfileDraft(ctx, local.ID, req, "same-draft-rebase")
	if e != nil || replayed.Revision != updated.Revision || !reflect.DeepEqual(replayed.Changes, updated.Changes) {
		t.Fatal("unknown response could not replay its original edit", e)
	}
	req.Changes[0].Roles[0].Role = "changed-body"
	if _, e = s.RebaseProfileDraft(ctx, local.ID, req, "same-draft-rebase"); !errors.Is(e, ErrConflict) {
		t.Fatal("same edit key accepted a different body", e)
	}
	if _, e = s.PreviewProfileDraft(ctx, local.ID); e != nil {
		t.Fatal("explicit same-draft rebase had no recovery exit", e)
	}
}

func TestDraftRebaseRejectsStaleHeadsWrongAssetsAndRollsBackReceiptFailure(t *testing.T) {
	for _, kind := range []string{"stale_revision", "forged_head", "extra_asset", "save_failure"} {
		t.Run(kind, func(t *testing.T) {
			s := collaborationStore(t)
			ctx := context.Background()
			snapshot := collaborationSnapshot(t, s)
			draft := mustDraft(t, s, snapshot, snapshot.Input.Assets[0].ID)
			base, e := s.ProfileDraftCurrentBase(ctx, draft.ID)
			if e != nil {
				t.Fatal(e)
			}
			req := RebaseDraftRequest{ExpectedDraftRevision: draft.Revision, ExpectedBaseRevisions: base.BaseRevisions, Changes: base.Changes}
			switch kind {
			case "stale_revision":
				req.ExpectedDraftRevision++
			case "forged_head":
				req.ExpectedBaseRevisions[0].Revision++
			case "extra_asset":
				req.Changes[0].AssetID = "unrelated-object"
			case "save_failure":
				if e = s.db.Exec("CREATE TRIGGER reject_rebase BEFORE INSERT ON profile_draft_rebase_requests BEGIN SELECT RAISE(ABORT,'PUBLIC_ARTIFICIAL_SAVE_FAILURE'); END").Error; e != nil {
					t.Fatal(e)
				}
			}
			if _, e = s.RebaseProfileDraft(ctx, draft.ID, req, "invalid-rebase"); e == nil {
				t.Fatal("unreviewed or failed rebase succeeded")
			}
			latest, e := s.GetProfileDraft(ctx, draft.ID)
			if e != nil || latest.Revision != draft.Revision || !reflect.DeepEqual(latest.Changes, draft.Changes) {
				t.Fatal("failed rebase committed an edit", e)
			}
		})
	}
}

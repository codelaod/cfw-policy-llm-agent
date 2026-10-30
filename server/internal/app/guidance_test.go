package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func guidanceContext() GuidanceContext {
	return GuidanceContext{Refs: GuidanceRefs{ConversationID: "conversation-local", IntentID: "intent-local", IntentRevision: 3, ProposalID: "proposal-current", PlanID: "plan-current", SnapshotID: "snapshot-fixed", JobID: "job-accepted", ReportID: "report-original", FindingID: "finding-original", DraftID: "draft-local", SubmissionID: "submission-original"}, Mode: "rules_only", DataPolicy: "prefer_fresh", CloudReady: true, ModelReady: true, Boundary: "internet", Direction: "in"}
}

func guidanceItem(t *testing.T, catalog GuidanceCatalog, id string) GuidanceItem {
	t.Helper()
	for _, item := range catalog.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("missing guidance %s in %+v", id, catalog.Items)
	return GuidanceItem{}
}

func guidanceContains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func assertGuidanceLocal(t *testing.T, catalog GuidanceCatalog) {
	t.Helper()
	for _, item := range catalog.Items {
		if item.Effects.AllowCloudReads || item.Effects.AllowModelRequests {
			t.Fatalf("local navigation/fill advertised external effects: %+v", item)
		}
		if item.Action.Type != "navigate_local" && item.Action.Type != "fill_input" {
			t.Fatal("local recovery produced an execution preview", item)
		}
	}
}

func TestGuidanceBlankAndNoConnectionKeepOfflineAndGoalPaths(t *testing.T) {
	catalog := BuildGuidance(GuidanceContext{})
	if catalog.State != "blank" || len(catalog.Items) != 3 {
		t.Fatal(catalog)
	}
	assertGuidanceLocal(t, catalog)
	guidanceItem(t, catalog, "GDE-01")
	guidanceItem(t, catalog, "GDE-11")
	start := guidanceItem(t, catalog, "GDE-START")
	if start.Intent.Mode != "rules_only" || start.Action.Target.Type != "composer" || start.State != "available" {
		t.Fatal("new conversation required a snapshot or model", start)
	}
}

func TestGuidanceClarificationAndReadyUseCurrentSavedReferences(t *testing.T) {
	context := guidanceContext()
	context.IntentStatus = "needs_clarification"
	clarify := BuildGuidance(context)
	assertGuidanceLocal(t, clarify)
	choice := guidanceItem(t, clarify, "GDE-CLARIFY")
	if choice.Action.Target.ID != context.Refs.IntentID || choice.Action.Target.IntentRevision != 3 {
		t.Fatal("candidate action lost intent revision", choice)
	}
	context.IntentStatus = "ready_for_confirmation"
	context.BusinessMissing = true
	ready := BuildGuidance(context)
	preview := guidanceItem(t, ready, "GDE-PREVIEW")
	if ready.State != "ready_for_confirmation" || preview.Action.Target.ID != context.Refs.PlanID || !preview.Confirmation.Required || !guidanceContains(preview.RequiredRefs, "proposal_id") || !guidanceContains(preview.Confirmation.Conditions, "accept_current_proposal_revision") {
		t.Fatal("ready guidance did not bind its saved proposal", preview)
	}
	guidanceItem(t, ready, "GDE-06")
	context.ProposalStatus = "expired"
	stale := guidanceItem(t, BuildGuidance(context), "GDE-PREPARE")
	if stale.Action.Target.Type != "intent" || stale.Action.Target.ID == context.Refs.PlanID || !guidanceContains(stale.Confirmation.Conditions, "review_changed_scope_and_versions") {
		t.Fatal("stale preview was offered for reuse", stale)
	}
}

func TestGuidanceActiveJobWaitCancelAndReconnectStayLocal(t *testing.T) {
	for _, state := range []string{"queued", "running", "cancelling"} {
		for _, disconnected := range []bool{false, true} {
			t.Run(state+map[bool]string{true: "-disconnected", false: "-connected"}[disconnected], func(t *testing.T) {
				context := guidanceContext()
				context.JobStatus, context.Phase = state, "model"
				context.RulesAvailable = true
				context.ObservationDisconnected = disconnected
				catalog := BuildGuidance(context)
				assertGuidanceLocal(t, catalog)
				guidanceItem(t, catalog, "GDE-12")
				if catalog.State != state {
					t.Fatal("waiting/cancellation became completed", catalog)
				}
				if disconnected {
					item := guidanceItem(t, catalog, "GDE-RECONNECT")
					if item.Action.Target.ID != context.Refs.JobID {
						t.Fatal("reconnect targeted a new execution")
					}
				}
				if state != "cancelling" {
					cancel := guidanceItem(t, catalog, "GDE-CANCEL")
					if cancel.Action.Target.ID != context.Refs.JobID || !cancel.Confirmation.Required || !guidanceContains(cancel.Confirmation.Conditions, "confirm_this_job_only") {
						t.Fatal("cancel was not bound to explicit current job", cancel)
					}
				}
			})
		}
	}
}

func TestGuidanceFailedCancelledAndInterruptedDoNotClaimCompletion(t *testing.T) {
	for _, state := range []string{"partial", "failed", "cancelled", "interrupted"} {
		t.Run(state, func(t *testing.T) {
			context := guidanceContext()
			context.JobStatus, context.RulesAvailable = state, true
			context.Refs.ReportID = ""
			catalog := BuildGuidance(context)
			if catalog.State != state {
				t.Fatal("terminal outcome was relabelled", catalog)
			}
			guidanceItem(t, catalog, "GDE-TERMINAL")
			guidanceItem(t, catalog, "GDE-RULES")
			retry := guidanceItem(t, catalog, "GDE-PREPARE")
			if !retry.Confirmation.Required || !guidanceContains(retry.Confirmation.Conditions, "accept_new_plan") || retry.Action.Target.ID == context.Refs.JobID {
				t.Fatal("terminal guidance silently replayed original execution", retry)
			}
			for _, item := range catalog.Items {
				if strings.Contains(item.DisplayText, "审计已完成") || strings.Contains(item.DisplayText, "全部安全") || strings.Contains(item.DisplayText, "已修复") {
					t.Fatal("partial/cancelled/failed outcome claimed success", item)
				}
			}
		})
	}
}

func TestGuidanceHistoryKeepsOriginalReportAndNeverReadsCloud(t *testing.T) {
	for _, saved := range []bool{false, true} {
		context := guidanceContext()
		context.Historical, context.HasSavedExplanation = true, saved
		context.Mode, context.ModelUpstreamState = "rules_and_agent", "not_checked"
		item := guidanceItem(t, BuildGuidance(context), "GDE-05")
		if item.Effects.AllowCloudReads || item.Effects.AllowModelRequests || item.Confirmation.Required || item.Intent.ContextReportID != context.Refs.ReportID || item.Intent.DataPolicy != "pinned_snapshot" {
			t.Fatal("historical explanation lost fixed input or requested current cloud", item)
		}
		if saved {
			if item.Behavior != "navigate_local" || item.Action.Target.ID != context.Refs.ReportID {
				t.Fatal("saved explanation was rerun", item)
			}
		} else if item.Behavior != "fill_input" || item.Intent.Mode != "rules_and_agent" || item.Intent.SnapshotID != context.Refs.SnapshotID || !strings.Contains(item.Intent.Text, context.Refs.ReportID) {
			t.Fatal("unsaved explanation did not prefill its precise historical context", item)
		}
	}
}

func TestGuidanceManualChangePreviewsForceRefreshAndPreservesDate(t *testing.T) {
	context := guidanceContext()
	context.ManualChange = true
	context.FreshAfter = "2026-10-29T17:12:13.123456789+08:00"
	item := guidanceItem(t, BuildGuidance(context), "GDE-09")
	if item.Intent.DataPolicy != "force_refresh" || item.Intent.FreshAfter != "2026-10-29T09:12:13.123456789Z" || item.Intent.RecheckOfJobID != context.Refs.JobID || !item.Confirmation.Required || !item.Effects.AllowCloudReads {
		t.Fatal("manual change did not bind a new force-refresh plan", item)
	}
	context.FreshAfter, context.CloudReady = "", false
	item = guidanceItem(t, BuildGuidance(context), "GDE-09")
	if item.State != "needs_input" || !guidanceContains(item.MissingInputs, "change_completed_at") || !guidanceContains(item.MissingInputs, "readonly_connection") || item.Effects.AllowCloudReads {
		t.Fatal("missing change time/connection was silently filled", item)
	}
}

func TestGuidanceAcknowledgementUnknownKeepsOriginalSubmission(t *testing.T) {
	context := guidanceContext()
	context.SubmissionUnknown = true
	context.IntentStatus = "ready_for_confirmation"
	catalog := BuildGuidance(context)
	assertGuidanceLocal(t, catalog)
	if catalog.State != "submission_unknown" || len(catalog.Items) != 2 {
		t.Fatal(catalog)
	}
	for _, item := range catalog.Items {
		if item.Action.Target.ID != context.Refs.SubmissionID {
			t.Fatal("unknown submission generated a new key", item)
		}
	}
	retry := guidanceItem(t, catalog, "GDE-SAME-REQUEST")
	if !retry.Confirmation.Required || !guidanceContains(retry.Confirmation.Conditions, "reuse_submission_id") || !guidanceContains(retry.Confirmation.Conditions, "keep_same_request_body") {
		t.Fatal("unknown submission lost exact retry conditions", retry)
	}
	context.Refs.SubmissionID = ""
	if item := guidanceItem(t, BuildGuidance(context), "GDE-SAME-REQUEST"); item.State != "needs_input" || !guidanceContains(item.MissingInputs, "submission_id") {
		t.Fatal("retry was available without original identifier", item)
	}
}

func TestGuidanceSaveFailureAndConflictKeepDraftAndRequireNewPreview(t *testing.T) {
	for _, state := range []string{"failed", "unsaved", "conflict"} {
		context := guidanceContext()
		context.SaveState = state
		catalog := BuildGuidance(context)
		assertGuidanceLocal(t, catalog)
		for _, item := range catalog.Items {
			if item.Action.Target.ID != context.Refs.DraftID {
				t.Fatal("save recovery lost original draft", item)
			}
		}
		if state == "conflict" {
			preview := guidanceItem(t, catalog, "GDE-DRAFT-PREVIEW")
			if !guidanceContains(preview.Confirmation.Conditions, "confirm_current_objects_and_base_versions") {
				t.Fatal("conflicted old preview remained usable", preview)
			}
		} else {
			retry := guidanceItem(t, catalog, "GDE-SAVE-RETRY")
			if !guidanceContains(retry.Confirmation.Conditions, "lookup_original_save_first") {
				t.Fatal("save failure became blind new write", retry)
			}
		}
	}
}

func TestGuidanceModeAndDataPolicyNeverWidenThroughFallback(t *testing.T) {
	context := guidanceContext()
	context.IntentStatus = "needs_data"
	context.Mode, context.ModelReady = "rules_and_agent", false
	item := guidanceItem(t, BuildGuidance(context), "GDE-PREPARE")
	if item.Intent.Mode != "rules_and_agent" || item.State != "needs_input" || !guidanceContains(item.MissingInputs, "model_available") || item.Effects.AllowModelRequests {
		t.Fatal("unavailable model silently changed the selected mode", item)
	}
	context.ModelReady, context.ModelUpstreamState = true, "not_checked"
	item = guidanceItem(t, BuildGuidance(context), "GDE-PREPARE")
	if !item.Effects.AllowModelRequests || item.State != "available" {
		t.Fatal("unverified yet ready selected model could not be previewed", item)
	}
	context.DataPolicy = "pinned_snapshot"
	item = guidanceItem(t, BuildGuidance(context), "GDE-PREPARE")
	if item.Effects.AllowCloudReads || item.Intent.DataPolicy != "pinned_snapshot" {
		t.Fatal("pinned preview acquired cloud effects", item)
	}
}

func TestGuidanceProfileDistinguishesUnknownRoleAndOutboundDependencies(t *testing.T) {
	context := guidanceContext()
	context.Surface, context.FactsAvailable = "profile", true
	item := guidanceItem(t, BuildGuidance(context), "GDE-03")
	if item.State != "needs_input" || !guidanceContains(item.MissingInputs, "model_mode_selection") || item.Effects.AllowModelRequests {
		t.Fatal("purpose suggestion implied selected model authorization", item)
	}
	baseline := guidanceItem(t, BuildGuidance(context), "GDE-04")
	if baseline.Intent.Mode != "rules_only" || baseline.Effects.AllowModelRequests {
		t.Fatal("unknown purpose erased independent baseline path", baseline)
	}
	context.Direction = "out"
	dependency := guidanceItem(t, BuildGuidance(context), "GDE-13")
	if dependency.Intent.Action != "edit_outbound_dependencies" || !guidanceContains(dependency.Confirmation.Conditions, "keep_outbound_intent_separate_from_inbound_sources") {
		t.Fatal("outbound suggestion reused inbound authorization", dependency)
	}
}

func TestGuidanceRestrictedAndImportRecoveryStayWithinLocalCapabilities(t *testing.T) {
	for _, restriction := range []string{"cloud_write", "out_of_scope", "unrelated", "sensitive", "external_instruction"} {
		context := guidanceContext()
		context.Restriction, context.FactsAvailable = restriction, true
		catalog := BuildGuidance(context)
		assertGuidanceLocal(t, catalog)
		encoded, _ := json.Marshal(catalog)
		for _, forbidden := range []string{"cloud_write\"", "云安全中心", "register_tool", "默认profile"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatal("restricted guidance introduced unsupported effects", forbidden)
			}
		}
	}
	context := guidanceContext()
	context.ImportState = "invalid"
	imported := BuildGuidance(context)
	assertGuidanceLocal(t, imported)
	guidanceItem(t, imported, "GDE-IMPORT-RETRY")
	context.ImportState, context.DataIncomplete = "partial", true
	facts := guidanceItem(t, BuildGuidance(context), "GDE-02")
	if !strings.Contains(facts.DisplayText, "信息不足") {
		t.Fatal("partial import implied complete observation", facts)
	}
}

func TestGuidanceCatalogIsVersionedBoundedDeterministicAndDetachesInvalidRefs(t *testing.T) {
	context := guidanceContext()
	context.JobStatus = "running"
	first, second := BuildGuidance(context), BuildGuidance(context)
	if !reflect.DeepEqual(first, second) || len(first.Items) > 3 || first.CatalogVersion != GuidanceCatalogVersion {
		t.Fatal("local catalog is unstable or unbounded")
	}
	seen := map[string]bool{}
	for _, item := range first.Items {
		if item.CatalogVersion != first.CatalogVersion || item.ID == "" || item.DisplayText == "" || item.Intent.Action == "" || item.Action.Target.Type == "" || item.Action.Type != item.Behavior || seen[item.ID] || item.MissingInputs == nil || item.RequiredRefs == nil || item.Confirmation.Conditions == nil {
			t.Fatal("incomplete versioned catalog item", item)
		}
		seen[item.ID] = true
	}
	context.Refs.JobID = "https://private.invalid/token\nexternal-instruction"
	invalid := BuildGuidance(context)
	for _, item := range invalid.Items {
		if item.State != "needs_input" || !guidanceContains(item.MissingInputs, "job_id") || item.Action.Target.ID != "" {
			t.Fatal("invalid reference became a target URL", item)
		}
	}
	encoded, _ := json.Marshal(invalid)
	if strings.Contains(string(encoded), "private.invalid") || strings.Contains(string(encoded), "external-instruction") {
		t.Fatal("untrusted reference was echoed")
	}
}

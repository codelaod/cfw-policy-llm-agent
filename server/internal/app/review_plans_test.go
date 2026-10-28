package app

import (
	"context"
	"errors"
	"testing"

	"cfw-policy-llm-agent/server/internal/storage"
)

func TestReviewAcceptedAssistantReplayPrecedesMutableConversationAndModelChecks(t *testing.T) {
	for _, change := range []string{"archive", "scope", "model_unavailable"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			store, service, snapshot, conversation, _ := planCase(t)
			service.ModelReady = true // Explicit synthetic test capability; no worker or adapter is started.
			engine := New(store, nil)
			defer engine.Stop()
			request := AssistantRequest{ClientMessageID: "review-accepted-message", PolicyScopeRef: snapshot.ScopeRef, SnapshotID: snapshot.ID, Text: "解释固定输入", ModelUseAccepted: true}
			job, message, replay, err := service.StartAssistant(ctx, engine, conversation.ID, request)
			if err != nil || replay {
				t.Fatal("fixture did not accept one local turn", err)
			}
			switch change {
			case "archive":
				archived := true
				_, err = store.PatchConversation(ctx, conversation.ID, storage.ConversationPatch{ExpectedRevision: conversation.Revision, Archived: &archived})
			case "scope":
				other, loadErr := NewAuditService(store).LoadExample(ctx, "internet-out")
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				_, err = store.PatchConversation(ctx, conversation.ID, storage.ConversationPatch{ExpectedRevision: conversation.Revision, PolicyScopeRef: &other.ScopeRef})
			case "model_unavailable":
				service.ModelReady = false
			}
			if err != nil {
				t.Fatal(err)
			}
			same, saved, replay, err := service.StartAssistant(ctx, engine, conversation.ID, request)
			if err != nil || !replay || same.ID != job.ID || saved.ID != message.ID {
				t.Fatalf("accepted message replay depends on later %s state: replay=%v err=%v", change, replay, err)
			}
			for _, altered := range []string{"text", "scope", "snapshot", "report", "consent"} {
				changed := request
				switch altered {
				case "text":
					changed.Text = "另一条有效请求"
				case "scope":
					changed.PolicyScopeRef = "scope_missing"
				case "snapshot":
					changed.SnapshotID = "snapshot_missing"
				case "report":
					changed.ReportID = "report_missing"
				case "consent":
					changed.ModelUseAccepted = false
				}
				if _, _, _, err := service.StartAssistant(ctx, engine, conversation.ID, changed); !errors.Is(err, storage.ErrConflict) {
					t.Fatalf("same key with changed %s was not an idempotency conflict after %s: %v", altered, change, err)
				}
			}
			jobs, err := store.ListJobs(ctx, conversation.ID, 0, 100)
			if err != nil || len(jobs) != 1 {
				t.Fatal("replay lookup created a fresh task", err)
			}
		})
	}
}

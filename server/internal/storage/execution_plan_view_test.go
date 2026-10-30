package storage

import (
	"context"
	"strings"
	"testing"
)

func TestPreparedContextNeverTreatsUnresolvedHistoryAsActualPartition(t *testing.T) {
	st, p, in, content := preparationCase(t, "nat-out", false)
	ctx := context.Background()
	if _, _, e := st.AppendConversationMessage(ctx, p.ConversationID, MessageInput{ClientMessageID: "unresolved-other-target", Role: "user", Origin: "user", PublicText: "UNRESOLVED_OTHER_BUSINESS_NOTE", PolicyScopeRef: ""}); e != nil {
		t.Fatal(e)
	}
	_, job, content := acceptPreparation(t, st, p, content)
	cloud, e := st.SavePreparedCloudSnapshot(ctx, job.ID, content, in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.BindPreparedAuditInput(ctx, job.ID, cloud.Content.SnapshotID); e != nil {
		t.Fatal(e)
	}
	window, e := st.ReadAgentContextWindow(ctx, job.ID)
	if e != nil {
		t.Fatal(e)
	}
	if window.AcceptedMessageID == "" || len(window.Messages) != 1 || window.Messages[0].ID != window.AcceptedMessageID {
		t.Fatal("accepted operation missing or unresolved pool included", window)
	}
	for _, m := range window.Messages {
		if strings.Contains(m.PublicText, "UNRESOLVED_OTHER_BUSINESS_NOTE") {
			t.Fatal("unresolved context disclosed across targets")
		}
	}
	raw, e := st.GetPlan(ctx, job.PlanID)
	if e != nil || raw.PolicyScopeRef != "" {
		t.Fatal("original request was relabelled", e)
	}
}

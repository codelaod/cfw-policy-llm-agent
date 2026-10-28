package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cfw-policy-llm-agent/server/internal/agentquery"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestReviewE3AssistantCancellationWhileWaitingToFinalizeDoesNotCommitNormalReply(t *testing.T) {
	store, plans, snapshot, conversation, _ := planCase(t)
	plans.ModelReady = true
	engine := New(store, nil)
	defer engine.Stop()
	job, _, _, err := plans.StartAssistant(context.Background(), engine, conversation.ID, AssistantRequest{ClientMessageID: "review-cancel-finalize", PolicyScopeRef: snapshot.ScopeRef, SnapshotID: snapshot.ID, Text: "解释固定资料", ModelUseAccepted: true})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := store.Claim(context.Background())
	if err != nil || !found {
		t.Fatal(err)
	}
	// Exhaust only the test pool after model generation. The resulting wait is
	// deterministic and occurs inside finalization, after prior tool events.
	store.SQL.SetMaxOpenConns(1)
	initialWaits := store.SQL.Stats().WaitCount
	held := make(chan *sql.Conn, 1)
	base := appAgentFactory(false)
	factory := AgentModelFactoryFunc(func(ctx context.Context, id string) (model.BaseChatModel, error) {
		inner, err := base(ctx, id)
		if err != nil {
			return nil, err
		}
		return &appFixtureModel{generate: func(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
			reply, err := inner.Generate(ctx, messages)
			if err == nil && len(reply.ToolCalls) == 0 {
				connection, holdErr := store.SQL.Conn(ctx)
				if holdErr != nil {
					return nil, holdErr
				}
				held <- connection
			}
			return reply, err
		}}, nil
	})
	workflow := NewWorkflow(store)
	workflow.Agent = NewAgentService(store, factory, plans.ConfigRevision, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- workflow.Run(ctx, job) }()
	var connection *sql.Conn
	select {
	case connection = <-held:
	case <-time.After(time.Second):
		t.Fatal("final model answer was not generated")
	}
	defer connection.Close()
	until := time.Now().Add(time.Second)
	for store.SQL.Stats().WaitCount == initialWaits {
		if time.Now().After(until) {
			t.Fatal("finalization did not wait for the held test connection")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	connection.Close()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("task cancellation was lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("finalization did not return")
	}
	current, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "completed" {
		t.Error("normal completion committed after task cancellation during database wait")
	}
	messages, err := store.ConversationMessages(context.Background(), conversation.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages.Items {
		if message.Origin == "model" {
			t.Error("late normal assistant reply committed after task cancellation")
		}
	}
}

func TestReviewE3CurrentUserObjectReferenceSurvivesHistoryProjection(t *testing.T) {
	store, _, snapshot, conversation, _ := planCase(t)
	queries, err := agentquery.New(snapshot.Input, nil)
	if err != nil {
		t.Fatal(err)
	}
	var initial agentquery.InitialView
	if err = json.Unmarshal([]byte(queries.InitialContext()), &initial); err != nil {
		t.Fatal(err)
	}
	job := acceptedContextAssistant(t, store, conversation, snapshot, "review-current-object", "请只解释 "+snapshot.Input.Assets[0].ID+" 的证据")
	value, err := BuildAgentContext(context.Background(), store, job.ID, queries.ProjectText)
	if err != nil {
		t.Fatal(err)
	}
	current := value.History[len(value.History)-1].Text
	if strings.Contains(current, snapshot.Input.Assets[0].ID) {
		t.Fatal("raw input object identity reached model history")
	}
	if !strings.Contains(current, initial.Assets[0]) {
		t.Fatalf("accepted current object reference was erased after safe projection: %s", current)
	}
}

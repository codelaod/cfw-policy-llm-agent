package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cfw-policy-llm-agent/server/internal/outbound"
)

func TestE3UsageCapturedFromPhysicalModelResponseBeforeSDKDefaults(t *testing.T) {
	for _, scenario := range []struct {
		name, body string
		status     int
		known      bool
		total      int64
	}{
		{"present", `{"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`, 200, true, 5},
		{"zero", `{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, 200, true, 0},
		{"missing", `{"choices":[]}`, 200, false, 0},
		{"partial", `{"usage":{"prompt_tokens":3,"total_tokens":3}}`, 200, false, 0},
		{"negative", `{"usage":{"prompt_tokens":-1,"completion_tokens":2,"total_tokens":1}}`, 200, false, 0},
		{"malformed", `{"usage":`, 200, false, 0},
		{"failed-response", `{"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`, 500, false, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store, plan, job := e3BudgetCase(t, 0, 3, 0, 3)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(scenario.status)
				io.WriteString(w, scenario.body)
			}))
			defer upstream.Close()
			manager, err := outbound.NewManager(store, outbound.Config{Revision: plan.ConfigRevision, Sources: []outbound.Source{{Ref: "configured-model", Class: "model", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []outbound.Route{{Method: "POST", Path: "/v1/chat/completions"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			client, err := manager.Client(ctx, job.ID, "configured-model")
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequestWithContext(ctx, "POST", upstream.URL+"/v1/chat/completions", strings.NewReader(`{}`))
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(outbound.ErrorCode(err))
			}
			response.Body.Close()
			attempts, err := store.JobAttempts(ctx, job.ID)
			if err != nil || len(attempts) != 1 || (attempts[0].Usage != nil) != scenario.known {
				t.Fatal("physical reported usage became SDK-default zero or was discarded", err)
			}
			if scenario.known && attempts[0].Usage.TotalTokens != scenario.total {
				t.Fatal("reported usage changed")
			}
			summary, err := store.BudgetUsage(ctx, plan.BudgetRef, "", job.ID)
			if err != nil {
				t.Fatal(err)
			}
			known, unknown := int64(0), int64(1)
			if scenario.known {
				known, unknown = 1, 0
			}
			if summary.KnownUsageResponses != known || summary.UnknownUsageAttempts != unknown || (summary.ReportedTokens != nil) != scenario.known {
				t.Fatal("summary hid an unknown usage response")
			}
			if scenario.name == "present" {
				var path string
				if err = store.SQL.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
					t.Fatal(err)
				}
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := Open(path, false)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				restored, err := reopened.JobAttempts(ctx, job.ID)
				if err != nil || len(restored) != 1 || restored[0].Usage == nil || restored[0].Usage.TotalTokens != 5 {
					t.Fatal("restart lost known provider counters", err)
				}
			}
		})
	}
}

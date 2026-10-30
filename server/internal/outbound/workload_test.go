package outbound

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExplicitRequestTimeoutAboveLegacyStillObeysTaskDeadline(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
			io.WriteString(w, `{}`)
		}
	}))
	defer upstream.Close()
	ledger := &testLedger{maximum: 2}
	manager, err := NewManager(ledger, Config{Revision: "explicit-timeout", Sources: []Source{{Ref: "model", Class: "model", Origin: upstream.URL, AllowLoopbackHTTP: true, Routes: []Route{{Method: "POST", Path: "/v1/chat/completions"}}, Timeout: time.Minute}}})
	if err != nil {
		t.Fatal("explicit request duration still hard-capped", err)
	}
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	client, err := manager.Client(ctx, "task-explicit", "model")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.Do(e3Request(t, ctx, "POST", upstream.URL+"/v1/chat/completions", `{}`))
	if err == nil || time.Since(started) > time.Second || ledger.count() != 1 {
		t.Fatal("root deadline or physical accounting lost")
	}
}

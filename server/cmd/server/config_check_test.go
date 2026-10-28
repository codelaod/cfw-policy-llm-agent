package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestE3ConfigCheckReadsOnlyExplicitSettingsWithoutExternalOrDatabaseWork(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer upstream.Close()
	t.Setenv("CFW_SERVER_TEST_KEY", "synthetic-server-private-key")
	data := `{"api_style":"chat_completions","provider_profile":"openai-compatible","base_url":"` + upstream.URL + `/v1","model":"synthetic-private-model","api_key_env":"CFW_SERVER_TEST_KEY","max_output_tokens":512,"timeout_seconds":10,"max_http_attempts":3,"enabled":true,"budget":{"ref":"explicit-config-test","expires_at":"2099-01-01T00:00:00Z","application_model_attempts":20,"session_model_attempts":7}}`
	path := filepath.Join(t.TempDir(), "private.json")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := checkModelConfig(path, &output); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || !strings.Contains(output.String(), `"configured":true`) {
		t.Fatal("configuration check executed a request or omitted status")
	}
	for _, private := range []string{"synthetic-server-private-key", "synthetic-private-model", upstream.URL, path, "CFW_SERVER_TEST_KEY"} {
		if strings.Contains(output.String(), private) {
			t.Fatal("configuration check exposed private settings")
		}
	}
}

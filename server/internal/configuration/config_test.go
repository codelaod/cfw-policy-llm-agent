package configuration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testValues() map[string]string {
	values := map[string]string{}
	for key, f := range fields() {
		values[key] = f.fallback
	}
	for key, value := range map[string]string{
		"CFW_APP_TOKEN":     "synthetic-local-application-token",
		"CFW_MODEL_ENABLED": "true", "CFW_MODEL_BASE_URL": "https://synthetic-model.example.invalid/custom/v1",
		"CFW_MODEL_NAME": "synthetic-existing-MiniMax", "CFW_MODEL_API_KEY": "synthetic-model-key", "CFW_MODEL_PROVIDER_PROFILE": "minimax",
		"CFW_CLOUD_ENABLED": "true", "CFW_CLOUD_EXPECTED_ACCOUNT_ID": "1234567890123456",
		"CFW_ALIYUN_ACCESS_KEY_ID": "synthetic-cloud-ak", "CFW_ALIYUN_ACCESS_KEY_SECRET": "synthetic-cloud-secret",
	} {
		values[key] = value
	}
	return values
}

func envFile(t *testing.T, values map[string]string) string {
	t.Helper()
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var body strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&body, "%s='%s'\n", key, values[key])
	}
	return rawFile(t, body.String())
}
func rawFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chosen config.env")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func comparableConfig(c Config) any {
	return []any{c.Token(), c.DBPath(), c.WebDir(), c.Model.Revision(), c.Model.APIKey(), c.Cloud.Revision(), c.Cloud.AccessKeyID(), c.Cloud.AccessKeySecret(), c.Cloud.SecurityToken(), c.Execution.Revision()}
}

func TestEveryRegisteredFieldUsesProcessPresenceAndEmptyOverride(t *testing.T) {
	values := testValues()
	path := envFile(t, values)
	cwd := filepath.Dir(path)
	for key := range fields() {
		t.Run(key, func(t *testing.T) {
			if _, err := load(path, map[string]string{key: "synthetic\x00private"}, cwd); err == nil || strings.Contains(err.Error(), "synthetic") {
				t.Fatal("process field not selected or private value leaked")
			}
			changed := testValues()
			changed[key] = ""
			expected, e1 := load("", changed, cwd)
			actual, e2 := load(path, map[string]string{key: ""}, cwd)
			// An empty process credential selects an entirely empty cloud record.
			if strings.HasPrefix(key, "CFW_ALIYUN_") {
				if e2 != nil || actual.Cloud.Ready() || actual.Cloud.AccessKeyID() != "" || actual.Cloud.AccessKeySecret() != "" {
					t.Fatal("empty process record fell back to file")
				}
				return
			}
			if (e1 == nil) != (e2 == nil) || (e1 == nil && !reflect.DeepEqual(comparableConfig(expected), comparableConfig(actual))) {
				t.Fatal("empty process value fell back to file or default", e1, e2)
			}
		})
	}
}

func TestCloudCredentialsAreChosenAsOneRecord(t *testing.T) {
	values := testValues()
	values["CFW_CLOUD_CREDENTIAL_MODE"] = "env_sts"
	values["CFW_ALIYUN_SECURITY_TOKEN"] = "synthetic-file-sts"
	path := envFile(t, values)
	for _, process := range []map[string]string{
		{"CFW_ALIYUN_ACCESS_KEY_ID": "process-ak"},
		{"CFW_ALIYUN_ACCESS_KEY_SECRET": "process-secret"},
		{"CFW_ALIYUN_SECURITY_TOKEN": ""},
		{"CFW_ALIYUN_ACCESS_KEY_ID": "process-ak", "CFW_ALIYUN_ACCESS_KEY_SECRET": "process-secret"},
	} {
		c, err := load(path, process, filepath.Dir(path))
		if err != nil || c.Cloud.Ready() || c.Cloud.AccessKeyID() != "" || c.Cloud.AccessKeySecret() != "" || c.Cloud.SecurityToken() != "" {
			t.Fatal("cloud identity was assembled from different sources", err)
		}
	}
	process := map[string]string{"CFW_ALIYUN_ACCESS_KEY_ID": "process-ak", "CFW_ALIYUN_ACCESS_KEY_SECRET": "process-secret", "CFW_ALIYUN_SECURITY_TOKEN": "process-token"}
	c, err := load(path, process, filepath.Dir(path))
	if err != nil || !c.Cloud.Ready() || c.Cloud.SecurityToken() != "process-token" {
		t.Fatal("complete process identity not selected", err)
	}
}

func TestNoAutomaticDotenvOrAmbientIdentityFallback(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".env.local"), []byte("CFW_APP_TOKEN=must-not-be-discovered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values := testValues()
	delete(values, "CFW_MODEL_API_KEY")
	delete(values, "CFW_ALIYUN_ACCESS_KEY_ID")
	delete(values, "CFW_ALIYUN_ACCESS_KEY_SECRET")
	values["CFW_APP_TOKEN"] = ""
	values["OPENAI_API_KEY"] = "must-not-be-used"
	values["MINIMAX_API_KEY"] = "must-not-be-used"
	values["ALIBABA_CLOUD_ACCESS_KEY_ID"] = "must-not-be-used"
	values["ALIBABA_CLOUD_ACCESS_KEY_SECRET"] = "must-not-be-used"
	c, err := load("", values, cwd)
	if err != nil || c.Token() != "" || c.Model.Ready() || c.Cloud.Ready() || c.Model.APIKey() != "" || c.Cloud.AccessKeyID() != "" {
		t.Fatal("implicit file or identity was selected", err)
	}
}

func TestLiteralDotenvSyntaxAndNoGlobalEnvironmentMutation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	literal := "$(touch " + marker + ")`command`$HOME#literal"
	path := rawFile(t, "\ufeff# comment\r\nexport CFW_APP_TOKEN='"+literal+"' # trailing comment\r\nCFW_WEB_DIR= # no static site\r\n")
	before := os.Environ()
	c, err := load(path, nil, filepath.Dir(path))
	if err != nil || c.Token() != literal || c.WebDir() != "" {
		t.Fatal("literal parsing failed", err)
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("literal executed")
	}
	if !reflect.DeepEqual(before, os.Environ()) {
		t.Fatal("loader mutated process environment")
	}
}

func TestRelativePathsUseTheirOwnSourceDirectory(t *testing.T) {
	path := envFile(t, map[string]string{"CFW_DB_PATH": "private folder/database.sqlite", "CFW_WEB_DIR": "web/dist"})
	cwd := t.TempDir()
	c, err := load(path, nil, cwd)
	if err != nil || c.DBPath() != filepath.Join(filepath.Dir(path), "private folder/database.sqlite") || c.WebDir() != filepath.Join(filepath.Dir(path), "web/dist") {
		t.Fatal("file paths use process cwd", err)
	}
	c, err = load(path, map[string]string{"CFW_DB_PATH": "other.sqlite", "CFW_WEB_DIR": ""}, cwd)
	if err != nil || c.DBPath() != filepath.Join(cwd, "other.sqlite") || c.WebDir() != "" {
		t.Fatal("process paths lost source or explicit blank", err)
	}
}

func TestRejectMalformedFilesEvenWhenProcessHasCompleteSettings(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate": "CFW_MODEL_NAME=one\nCFW_MODEL_NAME=two", "case": "cfw_model_name=private", "unknown": "CFW_UNKNOWN=private",
		"old": "CFW_MODEL_MAX_CALLS=4", "syntax": "not an assignment", "quote": "CFW_APP_TOKEN='unclosed", "tail": "CFW_APP_TOKEN='secret' trailing",
		"null": "CFW_APP_TOKEN=secret\x00", "oversize": strings.Repeat("#", maxFileBytes+1), "json": "{\"enabled\":true}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(rawFile(t, body), testValues(), t.TempDir()); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
				t.Fatal("malformed selected file ignored or error leaked input")
			}
		})
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing.env"), t.TempDir()} {
		if _, err := load(path, testValues(), t.TempDir()); err == nil {
			t.Fatal("bad selected source fell back")
		}
	}
}

func TestRejectFIFOWithoutWaitingForInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := load(path, nil, filepath.Dir(path)); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("configuration blocked on FIFO")
	}
}

func TestSafeFormattingOmitsPrivateValuesAndPaths(t *testing.T) {
	values := testValues()
	c, err := load("", values, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(c)
	output := string(encoded) + fmt.Sprintf("%v %+v %#v", c, c, c)
	for _, private := range []string{c.Model.APIKey(), c.Cloud.AccessKeyID(), c.Cloud.AccessKeySecret(), c.DBPath()} {
		if strings.Contains(output, private) {
			t.Fatal("private setting exposed")
		}
	}
	if c.Summary().Model.UpstreamValidation != "not_checked" || c.Summary().Model.LedgerState != "not_checked" {
		t.Fatal("local check claimed runtime verification")
	}
}

func mustCWD(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}

func TestSimpleExecutionSettingsAndSourceEquivalence(t *testing.T) {
	values := testValues()
	values["CFW_TASK_TIMEOUT_SECONDS"] = "600"
	values["CFW_MODEL_TIMEOUT_SECONDS"] = "60"
	values["CFW_AGENT_MAX_ROUNDS"] = "12"
	values["CFW_MODEL_MAX_OUTPUT_TOKENS"] = "4096"
	path := envFile(t, values)
	file, e := load(path, nil, filepath.Dir(path))
	if e != nil {
		t.Fatal(e)
	}
	process, e := load("", values, filepath.Dir(path))
	if e != nil || !reflect.DeepEqual(comparableConfig(file), comparableConfig(process)) {
		t.Fatal("settings differ by source", e)
	}
	for _, kind := range []string{"audit", "assistant_turn", "collect", "connection_check"} {
		l, e := file.Execution.Profile(kind)
		if e != nil || l.ExecutionSeconds != 600 || l.MaxRounds != 12 {
			t.Fatal("task uses hidden stage or request quota", kind, e)
		}
	}
	if !file.Model.Ready() || !file.Cloud.Ready() || file.Model.MaxOutputTokens() != 4096 {
		t.Fatal("credentials require an obsolete budget")
	}
	for _, key := range []string{"CFW_BUDGET_REF", "CFW_MODEL_MAX_HTTP_ATTEMPTS", "CFW_WORKLOAD_AUDIT_TOOL_CALLS"} {
		bad := testValues()
		bad[key] = "1"
		if _, e := load("", bad, t.TempDir()); e == nil {
			t.Fatal("obsolete field accepted")
		}
	}
}

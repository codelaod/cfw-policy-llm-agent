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
		"CFW_BUDGET_REF": "synthetic-shared-period", "CFW_BUDGET_APPLICATION_CLOUD_ATTEMPTS": "100", "CFW_BUDGET_SESSION_CLOUD_ATTEMPTS": "50",
		"CFW_BUDGET_APPLICATION_MODEL_ATTEMPTS": "20", "CFW_BUDGET_SESSION_MODEL_ATTEMPTS": "6",
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

func TestFileAndProcessResolveAllSettingsIdentically(t *testing.T) {
	values := testValues()
	path := envFile(t, values)
	cwd := filepath.Dir(path)
	file, err := load(path, nil, cwd)
	if err != nil {
		t.Fatal(err)
	}
	process, err := load("", values, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(comparableConfig(file), comparableConfig(process)) {
		t.Fatal("source changes effective configuration")
	}
	if !file.Model.Ready() || !file.Cloud.Ready() || !file.TokenReady() || file.Model.Model() != values["CFW_MODEL_NAME"] || file.Model.MaxOutputTokens() != 512 || file.Model.ProviderProfile() != "minimax" {
		t.Fatal("explicit settings lost")
	}
	if file.Model.Budget().Ref != file.Cloud.Budget().Ref || file.Cloud.Budget().Ref != file.Execution.Budget().Ref {
		t.Fatal("budgets diverged")
	}
	for _, kind := range kinds {
		p, e := file.Execution.Profile(kind)
		if e != nil || p.FinalizeSeconds != 10 {
			t.Fatal("missing explicit task protection", kind, e)
		}
	}
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

func TestProcessOverridesMetadataLimitsAndPaths(t *testing.T) {
	values := testValues()
	path := envFile(t, values)
	values["CFW_MODEL_MAX_OUTPUT_TOKENS"] = "128"
	values["CFW_MODEL_TIMEOUT_SECONDS"] = "120"
	values["CFW_CLOUD_ALLOWED_REGIONS"] = `["cn-hangzhou","cn-beijing"]`
	values["CFW_WORKLOAD_AUDIT_MODEL_HTTP_ATTEMPTS"] = "20"
	values["CFW_FRESHNESS_CONFIGURATION_MAX_AGE_SECONDS"] = "0"
	c, err := load(path, values, filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c.Execution.Profile("audit")
	f, _ := c.Execution.DataFreshness()
	if c.Model.MaxOutputTokens() != 128 || c.Model.Timeout() != 120*time.Second || len(c.Cloud.Regions()) != 2 || p.ModelHTTPAttempts != 20 || f.ConfigurationMaxAgeSeconds != 0 {
		t.Fatal("process overrides not applied")
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

func TestExplicitZeroDoesNotRefillBudgetsOrLimits(t *testing.T) {
	values := testValues()
	for _, key := range []string{"CFW_BUDGET_APPLICATION_MODEL_ATTEMPTS", "CFW_BUDGET_SESSION_MODEL_ATTEMPTS", "CFW_BUDGET_APPLICATION_CLOUD_ATTEMPTS", "CFW_BUDGET_SESSION_CLOUD_ATTEMPTS", "CFW_MODEL_MAX_HTTP_ATTEMPTS", "CFW_CLOUD_MAX_HTTP_ATTEMPTS", "CFW_WORKLOAD_AUDIT_MODEL_HTTP_ATTEMPTS", "CFW_WORKLOAD_AUDIT_CLOUD_HTTP_ATTEMPTS"} {
		values[key] = "0"
	}
	c, err := load("", values, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c.Execution.Profile("audit")
	if c.Model.Ready() || c.Cloud.Ready() || c.Model.MaxHTTPAttempts() != 0 || c.Cloud.MaxHTTPAttempts() != 0 || p.ModelHTTPAttempts != 0 || p.CloudHTTPAttempts != 0 || c.Execution.Budget().ApplicationModelAttempts != 0 {
		t.Fatal("zero silently increased")
	}
}

func TestAbsentBudgetDoesNotRecoverOrInventAuthorization(t *testing.T) {
	values := testValues()
	values["CFW_BUDGET_REF"] = ""
	for key := range values {
		if strings.HasPrefix(key, "CFW_BUDGET_") && strings.HasSuffix(key, "ATTEMPTS") {
			values[key] = "0"
		}
	}
	c, err := load("", values, t.TempDir())
	if err != nil || c.Execution.HasBudget() || c.Model.Ready() || c.Cloud.Ready() || c.Model.Budget().Ref != "" || c.Cloud.Budget().Ref != "" {
		t.Fatal("absent shared policy created authorization", err)
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

func TestRejectUnknownProcessFieldsAndUnsafeTypedValues(t *testing.T) {
	for key, value := range map[string]string{
		"CFW_UNKNOWN": "private-unrecognized", "cfw_model_name": "private-unrecognized", "CFW_MODEL_ENABLED": "1", "CFW_MODEL_MAX_HTTP_ATTEMPTS": "",
		"CFW_MODEL_TIMEOUT_SECONDS": "-1", "CFW_CLOUD_ALLOWED_REGIONS": "null", "CFW_CLOUD_ALLOWED_ASSETS": `[{"region":"cn-hangzhou","region":"cn-beijing"}]`,
		"CFW_MODEL_MAX_OUTPUT_TOKENS": "9223372036854775808", "CFW_WORKLOAD_AUDIT_EXECUTION_SECONDS": "1",
		"CFW_CLOUD_CONTROL_PLANE_REGION": "attacker.invalid", "CFW_MODEL_BASE_URL": "https://private:secret@example.invalid/v1",
		"CFW_BUDGET_SESSION_CLOUD_ATTEMPTS": "101", "CFW_FRESHNESS_VERSION": "", "CFW_DB_PATH": "",
	} {
		t.Run(key, func(t *testing.T) {
			values := testValues()
			values[key] = value
			if _, err := load("", values, t.TempDir()); err == nil || strings.Contains(err.Error(), "private-unrecognized") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "attacker") {
				t.Fatal("unsafe setting accepted or exposed")
			}
		})
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
	for _, private := range []string{values["CFW_APP_TOKEN"], values["CFW_MODEL_API_KEY"], values["CFW_MODEL_NAME"], values["CFW_MODEL_BASE_URL"], values["CFW_ALIYUN_ACCESS_KEY_ID"], values["CFW_ALIYUN_ACCESS_KEY_SECRET"], values["CFW_CLOUD_EXPECTED_ACCOUNT_ID"], values["CFW_BUDGET_REF"], c.DBPath()} {
		if strings.Contains(output, private) {
			t.Fatal("private setting exposed")
		}
	}
	if c.Summary().Model.UpstreamValidation != "not_checked" || c.Summary().Execution.LedgerState != "not_checked" {
		t.Fatal("local check claimed runtime verification")
	}
}

func TestPublicTemplateCoversExactlyTheUnifiedSchemaWithoutAuthorization(t *testing.T) {
	values, err := readEnv("../../../.env.example", fields())
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != len(fields()) {
		t.Fatal("template missing fields", len(values), len(fields()))
	}
	for key, spec := range fields() {
		if key != "CFW_DB_PATH" && key != "CFW_WEB_DIR" && values[key] != spec.fallback {
			t.Fatal("template and process defaults diverge", key)
		}
	}
	c, err := load("../../../.env.example", nil, mustCWD(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.Model.Ready() || c.Cloud.Ready() || c.Execution.HasBudget() || c.TokenReady() {
		t.Fatal("public template grants access")
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

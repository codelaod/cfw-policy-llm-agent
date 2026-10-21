package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const fixtureKey = "fixture-secret-never-real"

func configValues() map[string]string {
	return map[string]string{
		"CFW_MODEL_BASE_URL": "https://api.fixture.test/custom/v1/",
		"CFW_MODEL_API_KEY":  fixtureKey,
		"CFW_MODEL_NAME":     "fixture-model",
	}
}

func lookupValues(values map[string]string) envLookup {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func configFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestModelConfigFileFormatsAndNoGlobalEnvironmentMutation(t *testing.T) {
	content := "\ufeff# comment\r\nexport CFW_MODEL_BASE_URL = 'https://api.fixture.test/custom/v1/'\r\n" +
		"CFW_MODEL_API_KEY=\"" + fixtureKey + "\"\r\nCFW_MODEL_NAME=fixture-model\r\nOTHER_SERVICE_KEY=ignored\r\n"
	before, present := os.LookupEnv("CFW_MODEL_API_KEY")
	config, err := loadModelConfig(configFile(t, content), lookupValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	if config.baseURL != "https://api.fixture.test/custom/v1" || config.apiKey != fixtureKey || config.modelName != "fixture-model" {
		t.Fatal("incorrect config values")
	}
	if config.timeoutSeconds != 30 || config.maxCalls != 4 || config.maxOutputTokens != 512 {
		t.Fatal("incorrect protective defaults")
	}
	after, stillPresent := os.LookupEnv("CFW_MODEL_API_KEY")
	if before != after || present != stillPresent {
		t.Fatal("loader mutated the process environment")
	}
}

func TestModelConfigEnvironmentPrecedence(t *testing.T) {
	path := configFile(t, "CFW_MODEL_BASE_URL=https://file.fixture.test/v1\nCFW_MODEL_API_KEY=file-fixture\nCFW_MODEL_NAME=file-model\n")
	config, err := loadModelConfig(path, lookupValues(map[string]string{"CFW_MODEL_NAME": "environment-model"}))
	if err != nil || config.modelName != "environment-model" || config.apiKey != "file-fixture" {
		t.Fatalf("environment override failed: %v", err)
	}
	_, err = loadModelConfig(path, lookupValues(map[string]string{"CFW_MODEL_API_KEY": ""}))
	if err == nil || !strings.Contains(err.Error(), "CFW_MODEL_API_KEY") {
		t.Fatal("explicit empty environment value did not override file")
	}
	_, err = loadModelConfig(filepath.Join(t.TempDir(), "missing.env"), lookupValues(configValues()))
	if err != nil {
		t.Fatal("environment-only configuration must work without a file")
	}
}

func TestModelConfigMissingAndInvalidInputs(t *testing.T) {
	for _, key := range []string{"CFW_MODEL_BASE_URL", "CFW_MODEL_API_KEY", "CFW_MODEL_NAME"} {
		t.Run("missing_"+key, func(t *testing.T) {
			values := configValues()
			delete(values, key)
			_, err := loadModelConfig("", lookupValues(values))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatal("missing required field was accepted")
			}
		})
	}
	for _, key := range []string{"CFW_MODEL_TIMEOUT_SECONDS", "CFW_MODEL_MAX_CALLS", "CFW_MODEL_MAX_OUTPUT_TOKENS"} {
		for _, invalid := range []string{"0", "-1", "999999", fixtureKey} {
			t.Run(key+"_"+invalid, func(t *testing.T) {
				values := configValues()
				values[key] = invalid
				_, err := loadModelConfig("", lookupValues(values))
				if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), fixtureKey) {
					t.Fatal("invalid limit accepted or error exposed its value")
				}
			})
		}
	}
	values := configValues()
	values["CFW_MODEL_API_STYLE"] = fixtureKey
	_, err := loadModelConfig("", lookupValues(values))
	if err == nil || strings.Contains(err.Error(), fixtureKey) {
		t.Fatal("invalid protocol accepted or leaked")
	}
	values = configValues()
	values["CFW_MODEL_API_STYLE"] = "responses"
	if _, err := loadModelConfig("", lookupValues(values)); err != nil {
		t.Fatal("protocol metadata should be registrable without claiming compatibility")
	}
	for _, key := range []string{"contains space", "contains\nnewline", "contains\x01control", strings.Repeat("x", 8193)} {
		values = configValues()
		values["CFW_MODEL_API_KEY"] = key
		if _, err := loadModelConfig("", lookupValues(values)); err == nil {
			t.Fatal("invalid key format accepted")
		}
	}
}

func TestModelBaseURLValidationAndSanitizedErrors(t *testing.T) {
	for _, valid := range []string{"https://api.fixture.test/v1", "https://api.fixture.test/custom/compatible/v1/", "http://127.0.0.1:8000/v1", "http://localhost:8000/v1", "http://[::1]:8000/v1"} {
		if _, err := validateModelBaseURL(valid); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{
		"file:///tmp/config", "ftp://api.fixture.test", "http://api.fixture.test/v1", "not-a-url",
		"https://user:" + fixtureKey + "@api.fixture.test/v1", "https://api.fixture.test/v1?key=" + fixtureKey,
		"https://api.fixture.test/v1#" + fixtureKey, "https://api.fixture.test/v1/chat/completions", "https://api.fixture.test/v1/responses",
		"https://api.fixture.test/with space", "https://your-model-provider.example/v1",
	} {
		_, err := validateModelBaseURL(invalid)
		if err == nil || strings.Contains(err.Error(), fixtureKey) || strings.Contains(err.Error(), invalid) {
			t.Fatal("invalid URL accepted or error leaked its contents")
		}
	}
}

func TestModelEnvParserRejectsAmbiguityWithoutEchoingContents(t *testing.T) {
	for _, content := range []string{
		"CFW_MODEL_API_KEY=" + fixtureKey + "\nCFW_MODEL_API_KEY=duplicate",
		"CFW_MODEL_TYPO=" + fixtureKey, "CFW_MODEL_API_KEY=\"" + fixtureKey,
		fixtureKey, "CFW_MODEL_API_KEY=" + fixtureKey + "\x00",
	} {
		_, err := parseModelEnv(strings.NewReader(content))
		if err == nil || strings.Contains(err.Error(), fixtureKey) {
			t.Fatal("ambiguous config accepted or error exposed data")
		}
	}
	_, err := loadModelConfig(configFile(t, strings.Repeat("#", maxEnvFileBytes+1)), lookupValues(configValues()))
	if err == nil {
		t.Fatal("oversized config accepted")
	}
	_, err = loadModelConfig(t.TempDir(), lookupValues(configValues()))
	if err == nil {
		t.Fatal("non-regular config accepted")
	}
}

func TestModelEnvValuesAreNeverExecutedOrExpanded(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	literal := "$(touch " + marker + ")-${HOME}-`id`"
	values, err := parseModelEnv(strings.NewReader("CFW_MODEL_API_KEY='" + literal + "'"))
	if err != nil || values["CFW_MODEL_API_KEY"] != literal {
		t.Fatal("literal data was expanded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("config text was executed as shell")
	}
}

func TestModelConfigFormattingAndJSONAreRedacted(t *testing.T) {
	config, err := loadModelConfig("", lookupValues(configValues()))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{config.String(), config.GoString(), string(encoded), fmt.Sprintf("%v", config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config), fmt.Sprintf("%#v", &config)}
	for _, output := range outputs {
		for _, private := range []string{fixtureKey, "api.fixture.test", "fixture-model"} {
			if strings.Contains(output, private) {
				t.Fatal("config logging disclosed private data")
			}
		}
	}
}

func TestModelConfigCheckDoesNotSendRequests(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	values := configValues()
	values["CFW_MODEL_BASE_URL"] = server.URL + "/v1"
	var out, errors bytes.Buffer
	code := checkModelConfiguration("", lookupValues(values), &out, &errors)
	if code != 0 || errors.Len() != 0 {
		t.Fatal("valid config check failed")
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["config_valid"] != true || result["request_sent"] != false || requests.Load() != 0 {
		t.Fatal("config check sent a request or mislabeled its result")
	}
	if strings.Contains(out.String(), fixtureKey) || strings.Contains(out.String(), server.URL) {
		t.Fatal("check output exposed private fields")
	}
	out.Reset()
	errors.Reset()
	values["CFW_MODEL_MAX_CALLS"] = fixtureKey
	code = checkModelConfiguration("", lookupValues(values), &out, &errors)
	if code == 0 || out.Len() != 0 || strings.Contains(errors.String(), fixtureKey) {
		t.Fatal("invalid config check accepted or leaked data")
	}
}

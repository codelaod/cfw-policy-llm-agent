// Package configuration is the sole external configuration entrypoint.
// An explicitly selected dotenv file and process variables use identical fields
// and validation. Construction never changes os.Environ or contacts providers.
package configuration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/workload"
)

type Config struct {
	Model                         modelconfig.Config
	Cloud                         cloudconfig.Config
	Execution                     workload.Config
	token, dbPath, webDir, source string
}

type Summary struct {
	Source      string `json:"source"`
	Application struct {
		TokenState         string `json:"token_state"`
		DatabaseConfigured bool   `json:"database_configured"`
		WebConfigured      bool   `json:"web_configured"`
	} `json:"application"`
	Model     modelconfig.Summary `json:"model"`
	Cloud     cloudconfig.Summary `json:"cloud"`
	Execution workload.Summary    `json:"execution"`
}

func (c Config) Token() string  { return c.token }
func (c Config) DBPath() string { return c.dbPath }
func (c Config) WebDir() string { return c.webDir }
func (c Config) TokenReady() bool {
	return len(c.token) >= 16 && len(c.token) <= 8192 && strings.TrimSpace(c.token) == c.token
}
func (c Config) Summary() Summary {
	s := Summary{Source: c.source, Model: c.Model.Summary(), Cloud: c.Cloud.Summary(), Execution: c.Execution.Summary()}
	s.Application.TokenState = "missing"
	if c.token != "" {
		s.Application.TokenState = "invalid"
	}
	if c.TokenReady() {
		s.Application.TokenState = "available"
	}
	s.Application.DatabaseConfigured, s.Application.WebConfigured = c.dbPath != "", c.webDir != ""
	return s
}
func (c Config) MarshalJSON() ([]byte, error) { return json.Marshal(c.Summary()) }
func (c Config) String() string               { b, _ := c.MarshalJSON(); return string(b) }
func (c Config) GoString() string             { return c.String() }

type field struct{ kind, fallback string }

func fields() map[string]field {
	m := map[string]field{}
	add := func(kind, key, value string) { m[key] = field{kind, value} }
	for key, value := range map[string]string{
		"CFW_APP_TOKEN": "", "CFW_DB_PATH": "../data/private/e0.sqlite", "CFW_WEB_DIR": "",
		"CFW_MODEL_BASE_URL": "", "CFW_MODEL_API_KEY": "", "CFW_MODEL_NAME": "",
		"CFW_MODEL_API_STYLE": "chat_completions", "CFW_MODEL_PROVIDER_PROFILE": "openai-compatible",
		"CFW_CLOUD_CONNECTION_REF": "configured-aliyun", "CFW_CLOUD_CREDENTIAL_MODE": "env_ak",
		"CFW_CLOUD_EXPECTED_ACCOUNT_ID": "", "CFW_CLOUD_CONTROL_PLANE_REGION": "cn-hangzhou",
		"CFW_CLOUD_CREDENTIAL_EXPIRES_AT": "",
		"CFW_ALIYUN_ACCESS_KEY_ID":        "", "CFW_ALIYUN_ACCESS_KEY_SECRET": "", "CFW_ALIYUN_SECURITY_TOKEN": "",
	} {
		add("string", key, value)
	}
	for _, key := range []string{"CFW_MODEL_ENABLED", "CFW_CLOUD_ENABLED"} {
		add("bool", key, "false")
	}
	for key, value := range map[string]string{
		"CFW_MODEL_TIMEOUT_SECONDS": "30", "CFW_MODEL_MAX_OUTPUT_TOKENS": "512",
		"CFW_CLOUD_TIMEOUT_SECONDS":                   "30",
		"CFW_FRESHNESS_CONFIGURATION_MAX_AGE_SECONDS": "900", "CFW_FRESHNESS_METADATA_MAX_AGE_SECONDS": "3600",
	} {
		add("int", key, value)
	}
	for key, value := range map[string]string{
		"CFW_CLOUD_ALLOWED_REGIONS":    `["cn-hangzhou"]`,
		"CFW_CLOUD_ALLOWED_BOUNDARIES": `["internet/in","internet/out","nat/out"]`,
		"CFW_CLOUD_ALLOWED_ASSETS":     `[]`, "CFW_CLOUD_ALLOWED_NAT_GATEWAYS": `[]`,
	} {
		add("array", key, value)
	}
	add("int", "CFW_TASK_TIMEOUT_SECONDS", "240")
	add("int", "CFW_QUEUE_TIMEOUT_SECONDS", "120")
	add("int", "CFW_AGENT_MAX_ROUNDS", "6")
	return m
}

// Load reads only the requested file; an empty path selects process variables.
func Load(envFile string) (Config, error) {
	process := map[string]string{}
	for _, entry := range os.Environ() {
		k, v, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(k), "CFW_") {
			process[k] = v
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("CONFIG_WORKING_DIRECTORY_UNAVAILABLE")
	}
	return load(envFile, process, cwd)
}

func load(envFile string, process map[string]string, cwd string) (Config, error) {
	known := fields()
	fileValues := map[string]string{}
	fileDir := cwd
	var err error
	if envFile != "" {
		if !filepath.IsAbs(envFile) {
			envFile = filepath.Join(cwd, envFile)
		}
		fileDir = filepath.Dir(envFile)
		fileValues, err = readEnv(envFile, known)
		if err != nil {
			return Config{}, err
		}
	}
	// Reject misspelled project variables without echoing arbitrary input names.
	for key := range process {
		if strings.HasPrefix(strings.ToUpper(key), "CFW_") {
			if _, ok := known[key]; !ok {
				return Config{}, fmt.Errorf("CONFIG_UNKNOWN_ENVIRONMENT_FIELD")
			}
		}
	}
	// Cloud credentials form one record. Any process credential selects the whole
	// process tuple, including empty/missing members; never mix two identities.
	credentials := []string{"CFW_ALIYUN_ACCESS_KEY_ID", "CFW_ALIYUN_ACCESS_KEY_SECRET", "CFW_ALIYUN_SECURITY_TOKEN"}
	processCredentials := false
	for _, key := range credentials {
		if _, ok := process[key]; ok {
			processCredentials = true
		}
	}
	resolved := map[string]any{}
	for key, spec := range known {
		value := spec.fallback
		if v, ok := fileValues[key]; ok {
			value = v
		}
		if v, ok := process[key]; ok {
			value = v
		}
		if processCredentials && strings.HasPrefix(key, "CFW_ALIYUN_") {
			value = process[key]
		}
		if strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return Config{}, invalidField(key)
		}
		switch spec.kind {
		case "bool":
			if value != "true" && value != "false" {
				return Config{}, invalidField(key)
			}
			resolved[key] = value == "true"
		case "int":
			n, e := strconv.Atoi(value)
			if e != nil || strings.TrimSpace(value) != value || n < 0 {
				return Config{}, invalidField(key)
			}
			resolved[key] = n
		case "array":
			var a []any
			if !workload.ValidJSONDocument([]byte(value)) || json.Unmarshal([]byte(value), &a) != nil || a == nil {
				return Config{}, invalidField(key)
			}
			resolved[key] = a
		default:
			resolved[key] = value
		}
	}
	c := Config{token: resolved["CFW_APP_TOKEN"].(string), source: "environment"}
	if envFile != "" {
		c.source = "env_file_and_environment"
	}
	pathValue := func(key string) string {
		value := resolved[key].(string)
		if value == "" {
			return ""
		}
		base := cwd
		if _, fileSet := fileValues[key]; fileSet {
			if _, envSet := process[key]; !envSet {
				base = fileDir
			}
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(base, value)
		}
		return filepath.Clean(value)
	}
	c.dbPath, c.webDir = pathValue("CFW_DB_PATH"), pathValue("CFW_WEB_DIR")
	if c.dbPath == "" {
		return Config{}, invalidField("CFW_DB_PATH")
	}
	selectValues := func(prefix string, names ...string) map[string]any {
		out := map[string]any{}
		for _, name := range names {
			out[name] = resolved[prefix+strings.ToUpper(name)]
		}
		return out
	}
	limits := workload.Default("audit")
	limits.ExecutionSeconds = resolved["CFW_TASK_TIMEOUT_SECONDS"].(int)
	limits.QueueSeconds = resolved["CFW_QUEUE_TIMEOUT_SECONDS"].(int)
	limits.MaxRounds = resolved["CFW_AGENT_MAX_ROUNDS"].(int)
	c.Execution, err = workload.NewConfig(limits, &workload.Freshness{
		Version:                    "freshness-v1",
		ConfigurationMaxAgeSeconds: resolved["CFW_FRESHNESS_CONFIGURATION_MAX_AGE_SECONDS"].(int),
		MetadataMaxAgeSeconds:      resolved["CFW_FRESHNESS_METADATA_MAX_AGE_SECONDS"].(int),
	})
	if err != nil {
		return Config{}, err
	}
	lookup := func(key string) (string, bool) {
		value, ok := resolved[key]
		if !ok {
			return "", false
		}
		s, ok := value.(string)
		return s, ok
	}
	// Disabled blank adapters need no fabricated account, URL or model name.
	{
		model := selectValues("CFW_MODEL_", "enabled", "api_style", "provider_profile", "base_url", "timeout_seconds", "max_output_tokens")
		model["model"], model["api_key_env"] = resolved["CFW_MODEL_NAME"], "CFW_MODEL_API_KEY"
		body, _ := json.Marshal(model)
		c.Model, err = modelconfig.ParseSettings(body, lookup)
		if err != nil {
			return Config{}, err
		}
	}
	{
		cloud := selectValues("CFW_CLOUD_", "enabled", "connection_ref", "credential_mode", "credential_expires_at", "expected_account_id", "control_plane_region", "allowed_regions", "allowed_boundaries", "allowed_assets", "allowed_nat_gateways", "timeout_seconds")
		cloud["access_key_id_env"], cloud["access_key_secret_env"] = "CFW_ALIYUN_ACCESS_KEY_ID", "CFW_ALIYUN_ACCESS_KEY_SECRET"
		if cloud["credential_mode"] == "env_sts" {
			cloud["security_token_env"] = "CFW_ALIYUN_SECURITY_TOKEN"
		}
		body, _ := json.Marshal(cloud)
		c.Cloud, err = cloudconfig.ParseSettings(body, lookup)
		if err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

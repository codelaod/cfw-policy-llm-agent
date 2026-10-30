// Package modelconfig reads only an explicitly selected configuration source.
// It never discovers dotenv files or profiles and performs no network work.
package modelconfig

import (
	"bytes"
	"cfw-policy-llm-agent/server/internal/workload"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var ErrInvalid = errors.New("MODEL_CONFIG_INVALID")
var ErrCredential = errors.New("MODEL_CREDENTIAL_UNAVAILABLE")
var ErrSourceConflict = errors.New("MODEL_CONFIG_SOURCE_CONFLICT")

type Budget struct {
	Ref                      string `json:"ref"`
	ExpiresAt                string `json:"expires_at"`
	ApplicationModelAttempts int    `json:"application_model_attempts"`
	SessionModelAttempts     int    `json:"session_model_attempts"`
}
type fileConfig struct {
	APIStyle        string `json:"api_style"`
	ProviderProfile string `json:"provider_profile"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	APIKeyEnv       string `json:"api_key_env"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	MaxHTTPAttempts int    `json:"max_http_attempts"`
	Enabled         bool   `json:"enabled"`
	Budget          Budget `json:"budget"`
}

// Config keeps private source values inaccessible to JSON and formatting.
type Config struct {
	settings                    fileConfig
	key, endpoint, revision     string
	sourceKind, credentialState string
}
type Summary struct {
	Configured           bool   `json:"configured"`
	Enabled              bool   `json:"enabled"`
	Ready                bool   `json:"ready"`
	SourceSelected       bool   `json:"source_selected"`
	SourceKind           string `json:"source_kind,omitempty"`
	StructureValid       bool   `json:"structure_valid"`
	CredentialLocalReady bool   `json:"credential_local_ready"`
	CredentialState      string `json:"credential_state"`
	ExecutionPolicyReady bool   `json:"execution_policy_ready"`
	AuthorizationState   string `json:"authorization_state"`
	ProtocolSupported    bool   `json:"protocol_supported"`
	BlockedReason        string `json:"blocked_reason,omitempty"`
	LedgerState          string `json:"ledger_state"`
	UpstreamValidation   string `json:"upstream_validation"`
	APIStyle             string `json:"api_style"`
	ProviderProfile      string `json:"provider_profile"`
	MaxOutputTokens      int    `json:"max_output_tokens"`
	TimeoutSeconds       int    `json:"timeout_seconds"`
	MaxHTTPAttempts      int    `json:"max_http_attempts"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var ref = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type envLookup func(string) (string, bool)

// Load retains the explicit JSON entrypoint; operational blocks are reported
// in Summary rather than preventing the application's offline mode.
func Load(path string) (Config, error) { return loadJSON(path, os.LookupEnv) }

// LoadSelected rejects ambiguity before reading either source. An absent source
// is the ordinary offline configuration, not an ambient discovery request.
func LoadSelected(jsonPath, envFile string) (Config, error) {
	if jsonPath != "" && envFile != "" {
		return Config{}, ErrSourceConflict
	}
	if envFile != "" {
		return LoadEnvFile(envFile)
	}
	if jsonPath != "" {
		return Load(jsonPath)
	}
	return Config{}, nil
}
func readFile(path string, maximum int64) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalid
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || int64(len(body)) > maximum {
		return nil, ErrInvalid
	}
	return body, nil
}
func loadJSON(path string, lookup envLookup) (Config, error) {
	body, err := readFile(path, 32<<10)
	if err != nil || !uniqueJSON(body) {
		return Config{}, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	var input fileConfig
	if d.Decode(&input) != nil {
		return Config{}, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Config{}, ErrInvalid
	}
	c, err := build(input, "json")
	if err != nil {
		return Config{}, err
	}
	c.credentialState = "disabled"
	if input.Enabled {
		value, _ := lookup(input.APIKeyEnv)
		c.setCredential(value)
	}
	return c, nil
}
func validBudget(b Budget) bool {
	if b.Ref == "" {
		return b.ExpiresAt == "" && b.ApplicationModelAttempts == 0 && b.SessionModelAttempts == 0
	}
	if !ref.MatchString(b.Ref) || b.ApplicationModelAttempts < 0 || b.ApplicationModelAttempts > workload.MaxCounter || b.SessionModelAttempts < 0 || b.SessionModelAttempts > b.ApplicationModelAttempts {
		return false
	}
	if b.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, b.ExpiresAt); err != nil {
			return false
		}
	}
	return true
}
func build(input fileConfig, source string) (Config, error) {
	if (input.APIStyle != "chat_completions" && input.APIStyle != "responses") || (input.ProviderProfile != "openai-compatible" && input.ProviderProfile != "minimax") || input.Model == "" || len(input.Model) > 128 || strings.TrimSpace(input.Model) != input.Model || hasControl(input.Model) || !envName.MatchString(input.APIKeyEnv) || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 1024 || input.TimeoutSeconds < 1 || int64(input.TimeoutSeconds) > workload.MaxSeconds || input.MaxHTTPAttempts < 0 || input.MaxHTTPAttempts > workload.MaxCounter || !validBudget(input.Budget) {
		return Config{}, ErrInvalid
	}
	u, err := url.Parse(input.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || hasControl(input.BaseURL) || strings.TrimSpace(input.BaseURL) != input.BaseURL {
		return Config{}, ErrInvalid
	}
	if strings.Contains(u.Path, "//") || strings.Contains(u.Path, "..") || strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") {
		return Config{}, ErrInvalid
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	canonical, err := json.Marshal(input)
	if err != nil {
		return Config{}, ErrInvalid
	}
	digest := sha256.Sum256(append([]byte(source+"\x00"), canonical...))
	return Config{settings: input, endpoint: u.String(), revision: "model-config_" + hex.EncodeToString(digest[:])[:32], sourceKind: source}, nil
}
func hasControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func (c *Config) setCredential(value string) {
	c.key = ""
	c.credentialState = "missing"
	if value == "" {
		return
	}
	if strings.TrimSpace(value) != value || len(value) > 8192 || hasControl(value) {
		c.credentialState = "invalid"
		return
	}
	c.key = value
	c.credentialState = "available"
}

// WithBudget binds an explicitly resolved policy; it never invents a reference,
// expiry or allowance from a model file, key rotation or process environment.
func (c Config) WithBudget(b Budget) (Config, error) {
	if c.revision == "" || !validBudget(b) {
		return Config{}, ErrInvalid
	}
	input := c.settings
	input.Budget = b
	out, err := build(input, c.sourceKind)
	if err != nil {
		return Config{}, err
	}
	out.key, out.credentialState = c.key, c.credentialState
	return out, nil
}
func (c Config) WithProviderProfile(profile string) (Config, error) {
	input := c.settings
	input.ProviderProfile = profile
	out, err := build(input, c.sourceKind)
	if err != nil {
		return Config{}, err
	}
	out.key, out.credentialState = c.key, c.credentialState
	return out, nil
}
func (c Config) authorizationState() string {
	b := c.settings.Budget
	if b.Ref == "" {
		return "not_configured"
	}
	if b.ExpiresAt != "" {
		expiry, err := time.Parse(time.RFC3339Nano, b.ExpiresAt)
		if err != nil || !expiry.After(time.Now()) {
			return "expired"
		}
	}
	if b.ApplicationModelAttempts == 0 || b.SessionModelAttempts == 0 || c.settings.MaxHTTPAttempts == 0 {
		return "blocked"
	}
	return "active"
}
func (c Config) Model() string           { return c.settings.Model }
func (c Config) APIKey() string          { return c.key }
func (c Config) APIStyle() string        { return c.settings.APIStyle }
func (c Config) BaseURL() string         { return c.settings.BaseURL }
func (c Config) Endpoint() string        { return c.endpoint }
func (c Config) ProviderProfile() string { return c.settings.ProviderProfile }
func (c Config) MaxOutputTokens() int    { return c.settings.MaxOutputTokens }
func (c Config) Timeout() time.Duration {
	return time.Duration(c.settings.TimeoutSeconds) * time.Second
}
func (c Config) MaxHTTPAttempts() int   { return c.settings.MaxHTTPAttempts }
func (c Config) Budget() Budget         { return c.settings.Budget }
func (c Config) Revision() string       { return c.revision }
func (c Config) RequestedEnabled() bool { return c.settings.Enabled }
func (c Config) Ready() bool {
	return c.revision != "" && c.settings.Enabled && c.credentialState == "available" && c.settings.APIStyle == "chat_completions" && c.authorizationState() == "active"
}

// Enabled keeps its existing runtime gate semantics. Summary.Enabled records
// the requested toggle separately from Summary.Ready.
func (c Config) Enabled() bool { return c.Ready() }
func (c Config) Summary() Summary {
	s := Summary{Configured: c.revision != "", Enabled: c.RequestedEnabled(), Ready: c.Ready(), SourceSelected: c.revision != "", SourceKind: c.sourceKind, StructureValid: c.revision != "", CredentialLocalReady: c.credentialState == "available", CredentialState: c.credentialState, ExecutionPolicyReady: c.authorizationState() == "active", AuthorizationState: c.authorizationState(), ProtocolSupported: c.settings.APIStyle == "chat_completions", LedgerState: "not_checked", UpstreamValidation: "not_checked", APIStyle: c.settings.APIStyle, ProviderProfile: c.settings.ProviderProfile, MaxOutputTokens: c.settings.MaxOutputTokens, TimeoutSeconds: c.settings.TimeoutSeconds, MaxHTTPAttempts: c.settings.MaxHTTPAttempts}
	switch {
	case !s.Configured:
		s.BlockedReason = "not_configured"
		s.CredentialState = "not_checked"
	case !s.Enabled:
		s.BlockedReason = "disabled"
	case !s.CredentialLocalReady:
		s.BlockedReason = "credential_" + s.CredentialState
	case !s.ProtocolSupported:
		s.BlockedReason = "protocol_unsupported"
	case !s.ExecutionPolicyReady:
		s.BlockedReason = "authorization_" + s.AuthorizationState
	}
	return s
}
func (c Config) MarshalJSON() ([]byte, error) { return json.Marshal(c.Summary()) }
func (c Config) String() string               { b, _ := c.MarshalJSON(); return string(b) }
func (c Config) GoString() string             { return c.String() }

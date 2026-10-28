// Package modelconfig validates an explicitly selected private configuration.
// It never discovers profiles, reads dotenv files, or performs HTTP/DNS.
package modelconfig

import (
	"bytes"
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

// Config intentionally has no exported serializable private fields.
type Config struct {
	settings                fileConfig
	key, endpoint, revision string
}
type Summary struct {
	Configured      bool   `json:"configured"`
	Enabled         bool   `json:"enabled"`
	APIStyle        string `json:"api_style"`
	ProviderProfile string `json:"provider_profile"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	MaxHTTPAttempts int    `json:"max_http_attempts"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var ref = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func Load(path string) (Config, error) {
	var result Config
	if strings.TrimSpace(path) == "" {
		return result, ErrInvalid
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return result, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return result, ErrInvalid
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return result, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, 32<<10+1))
	if err != nil || len(body) > 32<<10 {
		return result, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	var input fileConfig
	if d.Decode(&input) != nil {
		return result, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return result, ErrInvalid
	}
	if input.APIStyle != "chat_completions" || (input.ProviderProfile != "openai-compatible" && input.ProviderProfile != "minimax") || input.Model == "" || len(input.Model) > 128 || strings.TrimSpace(input.Model) != input.Model || strings.ContainsAny(input.Model, "\r\n\x00") || !envName.MatchString(input.APIKeyEnv) || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 1024 || input.TimeoutSeconds < 1 || input.TimeoutSeconds > 30 || input.MaxHTTPAttempts < 1 || input.MaxHTTPAttempts > 6 {
		return result, ErrInvalid
	}
	u, err := url.Parse(input.BaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.ContainsAny(u.Host, "\r\n\x00") {
		return result, ErrInvalid
	}
	// A base URL identifies the provider's configured API namespace, not a
	// caller-controlled endpoint or a redirect target.
	if strings.Contains(u.Path, "//") || strings.Contains(u.Path, "..") || strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") {
		return result, ErrInvalid
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	b := input.Budget
	expires, err := time.Parse(time.RFC3339Nano, b.ExpiresAt)
	if !ref.MatchString(b.Ref) || err != nil || !expires.After(time.Now()) || b.ApplicationModelAttempts < 1 || b.ApplicationModelAttempts > 1000000 || b.SessionModelAttempts < 1 || b.SessionModelAttempts > b.ApplicationModelAttempts {
		return result, ErrInvalid
	}
	key := ""
	if input.Enabled {
		key = os.Getenv(input.APIKeyEnv)
		if strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key || len(key) > 8192 || strings.ContainsAny(key, "\r\n\x00") {
			return result, ErrCredential
		}
	}
	// The logical configuration is stable across key rotation. Its budget cycle
	// remains the explicitly configured Ref; no filename or key creates a reset.
	canonical, err := json.Marshal(input)
	if err != nil {
		return result, ErrInvalid
	}
	digest := sha256.Sum256(canonical)
	result = Config{settings: input, key: key, endpoint: u.String(), revision: "model-config_" + hex.EncodeToString(digest[:])[:32]}
	return result, nil
}

func (c Config) Model() string           { return c.settings.Model }
func (c Config) APIKey() string          { return c.key }
func (c Config) BaseURL() string         { return c.settings.BaseURL }
func (c Config) Endpoint() string        { return c.endpoint }
func (c Config) ProviderProfile() string { return c.settings.ProviderProfile }
func (c Config) MaxOutputTokens() int    { return c.settings.MaxOutputTokens }
func (c Config) Timeout() time.Duration {
	return time.Duration(c.settings.TimeoutSeconds) * time.Second
}
func (c Config) MaxHTTPAttempts() int { return c.settings.MaxHTTPAttempts }
func (c Config) Budget() Budget       { return c.settings.Budget }
func (c Config) Revision() string     { return c.revision }
func (c Config) Enabled() bool        { return c.settings.Enabled && c.key != "" }
func (c Config) Summary() Summary {
	return Summary{Configured: c.revision != "", Enabled: c.Enabled(), APIStyle: c.settings.APIStyle, ProviderProfile: c.settings.ProviderProfile, MaxOutputTokens: c.settings.MaxOutputTokens, TimeoutSeconds: c.settings.TimeoutSeconds, MaxHTTPAttempts: c.settings.MaxHTTPAttempts}
}

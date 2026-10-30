package workload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Budget matches persisted policy fields without importing storage, which can
// depend on Limits. Missing policy and explicit zero policy remain distinct.
type Budget struct {
	Ref                      string `json:"ref"`
	ExpiresAt                string `json:"expires_at,omitempty"`
	ApplicationCloudAttempts int64  `json:"application_cloud_attempts"`
	ApplicationModelAttempts int64  `json:"application_model_attempts"`
	SessionCloudAttempts     int64  `json:"session_cloud_attempts"`
	SessionModelAttempts     int64  `json:"session_model_attempts"`
}
type Freshness struct {
	Version                    string `json:"version"`
	ConfigurationMaxAgeSeconds int    `json:"configuration_max_age_seconds"`
	MetadataMaxAgeSeconds      int    `json:"metadata_max_age_seconds"`
}
type Config struct {
	freshness                             *Freshness
	profileRef, providerProfile, revision string
	profiles                              map[string]Limits
	budget                                *Budget
}
type Resolved struct {
	ProfileRef string `json:"profile_ref"`
	Legacy     bool   `json:"legacy"`
	Limits     Limits `json:"limits"`
}
type Summary struct {
	FreshnessConfigured bool `json:"freshness_configured"`

	Configured           bool   `json:"configured"`
	ProfileCount         int    `json:"profile_count"`
	BudgetConfigured     bool   `json:"budget_configured"`
	ModelProviderProfile string `json:"model_provider_profile,omitempty"`
	LedgerState          string `json:"ledger_state"`
	UpstreamValidation   string `json:"upstream_validation"`
}

var alias = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, ErrInvalid
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Config{}, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, ErrInvalid
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Config{}, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(body) > 64<<10 || !uniqueJSON(body) {
		return Config{}, ErrInvalid
	}
	var raw struct {
		SchemaVersion   int                        `json:"schema_version"`
		ProfileRef      string                     `json:"profile_ref"`
		Profiles        map[string]json.RawMessage `json:"profiles"`
		Budget          *Budget                    `json:"budget,omitempty"`
		ProviderProfile string                     `json:"model_provider_profile,omitempty"`
		Freshness       *Freshness                 `json:"data_freshness,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&raw) != nil || raw.SchemaVersion != 1 || (!alias.MatchString(raw.ProfileRef) || raw.ProfileRef == LegacyProfileRef) || len(raw.Profiles) != 4 {
		return Config{}, ErrInvalid
	}
	if raw.ProviderProfile != "" && raw.ProviderProfile != "minimax" && raw.ProviderProfile != "openai-compatible" {
		return Config{}, ErrInvalid
	}
	out := Config{profileRef: raw.ProfileRef, providerProfile: raw.ProviderProfile, profiles: map[string]Limits{}, budget: raw.Budget, freshness: raw.Freshness}
	for kind, data := range raw.Profiles {
		if !publicKind(kind) {
			return Config{}, ErrInvalid
		}
		var values map[string]json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return Config{}, ErrInvalid
		}
		typ := reflect.TypeOf(Limits{})
		if len(values) != typ.NumField() {
			return Config{}, ErrInvalid
		}
		for i := 0; i < typ.NumField(); i++ {
			if value, ok := values[typ.Field(i).Tag.Get("json")]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return Config{}, ErrInvalid
			}
		}
		var limits Limits
		if json.Unmarshal(data, &limits) != nil || limits.Validate(kind) != nil || limits.FinalizeSeconds <= 0 {
			return Config{}, ErrInvalid
		}
		out.profiles[kind] = limits
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return Config{}, ErrInvalid
	}
	if budgetJSON, exists := envelope["budget"]; exists {
		if bytes.Equal(bytes.TrimSpace(budgetJSON), []byte("null")) {
			return Config{}, ErrInvalid
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(budgetJSON, &fields) != nil {
			return Config{}, ErrInvalid
		}
		for _, key := range []string{"application_cloud_attempts", "application_model_attempts", "session_cloud_attempts", "session_model_attempts"} {
			if value, exists := fields[key]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return Config{}, ErrInvalid
			}
		}
	}
	if value, present := envelope["data_freshness"]; present {
		var fields map[string]json.RawMessage
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &fields) != nil || len(fields) != 3 {
			return Config{}, ErrInvalid
		}
		for _, key := range []string{"version", "configuration_max_age_seconds", "metadata_max_age_seconds"} {
			if v, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return Config{}, ErrInvalid
			}
		}
	}
	if raw.Freshness != nil {
		f := raw.Freshness
		if !alias.MatchString(f.Version) {
			return Config{}, ErrInvalid
		}
		if _, e := Seconds(f.ConfigurationMaxAgeSeconds); e != nil {
			return Config{}, ErrInvalid
		}
		if _, e := Seconds(f.MetadataMaxAgeSeconds); e != nil {
			return Config{}, ErrInvalid
		}
	}
	if raw.Budget != nil {
		b := raw.Budget
		if !alias.MatchString(b.Ref) {
			return Config{}, ErrInvalid
		}
		for _, v := range []int64{b.ApplicationCloudAttempts, b.ApplicationModelAttempts, b.SessionCloudAttempts, b.SessionModelAttempts} {
			if v < 0 || v > MaxCounter {
				return Config{}, ErrInvalid
			}
		}
		if b.SessionCloudAttempts > b.ApplicationCloudAttempts || b.SessionModelAttempts > b.ApplicationModelAttempts {
			return Config{}, ErrInvalid
		}
		if b.ExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, b.ExpiresAt); err != nil {
				return Config{}, ErrInvalid
			}
		}
	}
	canonical, err := json.Marshal(raw)
	if err != nil {
		return Config{}, ErrInvalid
	}
	sum := sha256.Sum256(canonical)
	out.revision = "execution-config_" + hex.EncodeToString(sum[:])[:32]
	return out, nil
}
func (c Config) Configured() bool             { return c.revision != "" }
func (c Config) ProfileRef() string           { return c.profileRef }
func (c Config) Revision() string             { return c.revision }
func (c Config) ModelProviderProfile() string { return c.providerProfile }
func (c Config) HasBudget() bool              { return c.budget != nil }
func (c Config) Budget() Budget {
	if c.budget == nil {
		return Budget{}
	}
	return *c.budget
}
func (c Config) Profile(kind string) (Limits, error) {
	if !publicKind(kind) {
		return Limits{}, ErrInvalid
	}
	if !c.Configured() {
		return Default(kind), nil
	}
	l, ok := c.profiles[kind]
	if !ok {
		return Limits{}, ErrInvalid
	}
	return l, nil
}
func (c Config) Resolve(kind string, ceilings Ceilings) (Resolved, error) {
	l, err := c.Profile(kind)
	if err != nil {
		return Resolved{}, err
	}
	l, err = l.Constrain(kind, ceilings)
	if err != nil {
		return Resolved{}, err
	}
	ref := c.profileRef
	if !c.Configured() {
		ref = LegacyProfileRef
	}
	return Resolved{ProfileRef: ref, Legacy: !c.Configured(), Limits: l}, nil
}
func (c Config) Summary() Summary {
	return Summary{FreshnessConfigured: c.freshness != nil, Configured: c.Configured(), ProfileCount: len(c.profiles), BudgetConfigured: c.HasBudget(), ModelProviderProfile: c.providerProfile, LedgerState: "not_checked", UpstreamValidation: "not_checked"}
}
func (c Config) MarshalJSON() ([]byte, error) { return json.Marshal(c.Summary()) }
func (c Config) String() string               { b, _ := c.MarshalJSON(); return string(b) }
func (c Config) GoString() string             { return c.String() }
func uniqueJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	var visit func(int) bool
	visit = func(depth int) bool {
		if depth > 20 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		if delim == '{' {
			keys := []string{}
			for d.More() {
				k, err := d.Token()
				key, ok := k.(string)
				if err != nil || !ok {
					return false
				}
				for _, previous := range keys {
					if strings.EqualFold(previous, key) {
						return false
					}
				}
				keys = append(keys, key)
				if !visit(depth + 1) {
					return false
				}
			}
		} else if delim == '[' {
			for d.More() {
				if !visit(depth + 1) {
					return false
				}
			}
		} else {
			return false
		}
		_, err = d.Token()
		return err == nil
	}
	if !visit(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

// Normalize makes only an entirely absent old workload an explicit legacy
// binding. A labelled profile, including all its zero ceilings, is never
// refilled from defaults. It can be persisted inside an existing plan body.
func Normalize(kind string, value Resolved) (Resolved, error) {
	if value == (Resolved{}) {
		value = Resolved{ProfileRef: LegacyProfileRef, Legacy: true, Limits: Default(kind)}
	}
	if !alias.MatchString(value.ProfileRef) || value.Limits.Validate(kind) != nil {
		return Resolved{}, ErrInvalid
	}
	if value.Legacy {
		if value.ProfileRef != LegacyProfileRef {
			return Resolved{}, ErrInvalid
		}
		base, current := reflect.ValueOf(Default(kind)), reflect.ValueOf(value.Limits)
		for i := 0; i < base.NumField(); i++ {
			if current.Field(i).Int() > base.Field(i).Int() {
				return Resolved{}, ErrInvalid
			}
		}
	} else if value.ProfileRef == LegacyProfileRef {
		return Resolved{}, ErrInvalid
	}
	return value, nil
}
func (r Resolved) Validate(kind string) error { _, err := Normalize(kind, r); return err }

// ValidJSONDocument rejects duplicate/case-folded keys, excessive nesting and
// multiple JSON values. The caller still validates its typed non-null fields.
func ValidJSONDocument(data []byte) bool { return uniqueJSON(data) }

func (c Config) DataFreshness() (Freshness, bool) {
	if c.freshness == nil {
		return Freshness{}, false
	}
	return *c.freshness, true
}

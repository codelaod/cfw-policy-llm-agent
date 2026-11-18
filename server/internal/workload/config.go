package workload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

type Freshness struct {
	Version                    string `json:"version"`
	ConfigurationMaxAgeSeconds int    `json:"configuration_max_age_seconds"`
	MetadataMaxAgeSeconds      int    `json:"metadata_max_age_seconds"`
}
type Config struct {
	freshness *Freshness
	limits    Limits
	revision  string
}
type Resolved struct {
	ProfileRef string `json:"profile_ref"`
	Limits     Limits `json:"limits"`
}
type Summary struct {
	FreshnessConfigured bool `json:"freshness_configured"`
	Configured          bool `json:"configured"`
}

var alias = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// NewConfig validates one shared set of task settings for all public task kinds.
func NewConfig(limits Limits, freshness *Freshness) (Config, error) {
	if limits.Validate("audit") != nil {
		return Config{}, ErrInvalid
	}
	if freshness != nil {
		f := *freshness
		if !alias.MatchString(f.Version) {
			return Config{}, ErrInvalid
		}
		if _, e := Seconds(f.ConfigurationMaxAgeSeconds); e != nil {
			return Config{}, ErrInvalid
		}
		if _, e := Seconds(f.MetadataMaxAgeSeconds); e != nil {
			return Config{}, ErrInvalid
		}
		freshness = &f
	}
	body, err := json.Marshal(struct {
		Limits    Limits     `json:"limits"`
		Freshness *Freshness `json:"data_freshness,omitempty"`
	}{limits, freshness})
	if err != nil {
		return Config{}, ErrInvalid
	}
	sum := sha256.Sum256(body)
	return Config{limits: limits, freshness: freshness, revision: "execution-config_" + hex.EncodeToString(sum[:])[:32]}, nil
}
func (c Config) Configured() bool { return c.revision != "" }
func (c Config) Revision() string { return c.revision }
func (c Config) Profile(kind string) (Limits, error) {
	if !publicKind(kind) {
		return Limits{}, ErrInvalid
	}
	if !c.Configured() {
		return Default(kind), nil
	}
	return c.limits, nil
}
func (c Config) Summary() Summary {
	return Summary{FreshnessConfigured: c.freshness != nil, Configured: c.Configured()}
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

// Validate checks the frozen execution settings.
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

func (c Config) Resolve(kind string) (Resolved, error) {
	l, e := c.Profile(kind)
	return Resolved{ProfileRef: DefaultProfileRef, Limits: l}, e
}
func Normalize(kind string, r Resolved) (Resolved, error) {
	if r == (Resolved{}) {
		r = Resolved{ProfileRef: DefaultProfileRef, Limits: Default(kind)}
	}
	if !alias.MatchString(r.ProfileRef) || r.Limits.Validate(kind) != nil {
		return Resolved{}, ErrInvalid
	}
	return r, nil
}

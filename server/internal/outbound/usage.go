package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"cfw-policy-llm-agent/server/internal/domain"
)

// UsageLedger records the result and optional provider counters atomically.
// Compatibility ledgers may implement only Ledger; no SDK zero values are used
// to synthesize counters on that path.
type UsageLedger interface {
	RecordAttemptResultWithUsage(context.Context, string, string, string, int, int64, *domain.ModelUsage) error
}

func recordResult(ctx context.Context, ledger Ledger, id, state, code string, status int, duration int64, usage *domain.ModelUsage) error {
	if withUsage, ok := ledger.(UsageLedger); ok {
		return withUsage.RecordAttemptResultWithUsage(ctx, id, state, code, status, duration, usage)
	}
	return ledger.RecordAttemptResult(ctx, id, state, code, status, duration)
}

func usageObject(data []byte) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		name, ok := field.(string)
		if !ok {
			return nil, false
		}
		if _, exists := fields[name]; exists {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, false
	}
	return fields, true
}

// ParseModelUsage reads the bounded successful HTTP body before SDK decoding.
// All three pointers must be present and valid integer counters. Ambiguous,
// partial, missing or malformed usage remains unknown rather than becoming 0.
func ParseModelUsage(body []byte) *domain.ModelUsage {
	if len(body) == 0 || len(body) > 2<<20 {
		return nil
	}
	root, ok := usageObject(body)
	if !ok {
		return nil
	}
	fields, ok := usageObject(root["usage"])
	if !ok {
		return nil
	}
	var wire struct{ Prompt, Completion, Total *int64 }
	for _, field := range []struct {
		name   string
		target **int64
	}{{"prompt_tokens", &wire.Prompt}, {"completion_tokens", &wire.Completion}, {"total_tokens", &wire.Total}} {
		raw, present := fields[field.name]
		if !present {
			return nil
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			return nil
		}
		for _, digit := range raw {
			if digit < '0' || digit > '9' {
				return nil
			}
		}
		if json.Unmarshal(raw, field.target) != nil || *field.target == nil {
			return nil
		}
	}
	usage := &domain.ModelUsage{PromptTokens: *wire.Prompt, CompletionTokens: *wire.Completion, TotalTokens: *wire.Total}
	if !usage.Valid() {
		return nil
	}
	return usage
}

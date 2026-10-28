package outbound

import (
	"strings"
	"testing"
)

func TestE3UsageOnlyCompleteExplicitIntegerCountersAreKnown(t *testing.T) {
	for _, test := range []struct {
		name, body string
		known      bool
		total      int64
	}{
		{"reported", `{"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":9}}`, true, 9},
		{"explicit-zero", `{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, true, 0},
		{"missing", `{"choices":[]}`, false, 0},
		{"null", `{"usage":null}`, false, 0},
		{"partial", `{"usage":{"prompt_tokens":3,"total_tokens":3}}`, false, 0},
		{"null-counter", `{"usage":{"prompt_tokens":null,"completion_tokens":2,"total_tokens":2}}`, false, 0},
		{"negative", `{"usage":{"prompt_tokens":-1,"completion_tokens":2,"total_tokens":1}}`, false, 0},
		{"negative-zero", `{"usage":{"prompt_tokens":-0,"completion_tokens":0,"total_tokens":0}}`, false, 0},
		{"string", `{"usage":{"prompt_tokens":"3","completion_tokens":2,"total_tokens":5}}`, false, 0},
		{"fraction", `{"usage":{"prompt_tokens":3.0,"completion_tokens":2,"total_tokens":5}}`, false, 0},
		{"exponent", `{"usage":{"prompt_tokens":3e0,"completion_tokens":2,"total_tokens":5}}`, false, 0},
		{"unsafe-json-integer", `{"usage":{"prompt_tokens":9007199254740992,"completion_tokens":0,"total_tokens":9007199254740992}}`, false, 0},
		{"int64-overflow", `{"usage":{"prompt_tokens":999999999999999999999,"completion_tokens":0,"total_tokens":0}}`, false, 0},
		{"duplicate-counter", `{"usage":{"prompt_tokens":-1,"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, false, 0},
		{"duplicate-usage", `{"usage":null,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, false, 0},
		{"invalid-json", `{"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}`, false, 0},
		{"trailing-json", `{"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}} {}`, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage := ParseModelUsage([]byte(test.body))
			if (usage != nil) != test.known {
				t.Fatal("missing/malformed usage changed its known state")
			}
			if usage != nil && usage.TotalTokens != test.total {
				t.Fatal("reported total was recomputed")
			}
		})
	}
	if ParseModelUsage([]byte(strings.Repeat(" ", 2<<20)+`{"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`)) != nil {
		t.Fatal("oversized usage body parsed")
	}
}

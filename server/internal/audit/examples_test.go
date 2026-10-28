package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Regeneration is explicit and only writes these public, synthetic files:
// CFW_WRITE_PUBLIC_FIXTURES=1 go test ./internal/audit -run TestPublicExamplesMatch
func TestPublicExamplesMatch(t *testing.T) {
	for _, id := range []string{"internet-in", "internet-out", "nat-out"} {
		t.Run(id, func(t *testing.T) {
			in, err := SyntheticExample(id)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("../../../testdata/public", id+".json")
			if os.Getenv("CFW_WRITE_PUBLIC_FIXTURES") == "1" {
				if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				b, _ := json.MarshalIndent(in, "", "  ")
				if err = os.WriteFile(path, append(b, '\n'), 0644); err != nil {
					t.Fatal(err)
				}
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fromFile any
			var fromBuiltIn any
			if err = json.Unmarshal(b, &fromFile); err != nil {
				t.Fatal(err)
			}
			built, _ := json.Marshal(in)
			json.Unmarshal(built, &fromBuiltIn)
			if !reflect.DeepEqual(fromFile, fromBuiltIn) {
				t.Fatal("public fixture differs from the fixed built-in construction")
			}
			r, err := Run(context.Background(), in)
			if err != nil || len(r.Findings) == 0 {
				t.Fatalf("public example not runnable: %v", err)
			}
			if id == "internet-in" {
				if find(t, r, "CHK-07").Verdict != "fail" {
					t.Fatal("manual label: SSH wildcard should fail")
				}
			} else if id == "internet-out" {
				if find(t, r, "OUT-01").Verdict != "fail" || find(t, r, "OUT-02").Verdict != "fail" {
					t.Fatal("manual label: outbound excess and required refusal should fail")
				}
			} else {
				if find(t, r, "NAT-01").Verdict != "pass" {
					t.Fatal("manual label: synthetic NAT relation should pass")
				}
			}
		})
	}
}

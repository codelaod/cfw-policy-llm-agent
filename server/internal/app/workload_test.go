package app

import (
	"cfw-policy-llm-agent/server/internal/workload"
	"testing"
)

func explicitWorkloads(t *testing.T) workload.Config {
	t.Helper()
	limits := workload.Default("audit")
	limits.MaxRounds = 12
	c, err := workload.NewConfig(limits, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Package workload resolves explicit task protection without credentials,
// provider clients, persistence side effects or implicit authorizations.
package workload

import (
	"errors"
	"math"
	"time"
)

const LegacyProfileRef = "legacy-hardcoded-v1"
const MaxSeconds = int64(math.MaxInt64) / int64(time.Second)
const MaxCounter = 1_000_000_000

var ErrInvalid = errors.New("EXECUTION_CONFIG_INVALID")

// Limits are maxima, not execution promises or supplier service levels. Zero
// counters forbid attempts; a zero queue window forbids waiting. A zero unused
// stage disables it. FinalizeSeconds=0 is legacy shared-root finalization,
// never an unlimited timeout; explicit profiles should reserve this phase.
type Limits struct {
	QueueSeconds        int `json:"queue_seconds"`
	ExecutionSeconds    int `json:"execution_seconds"`
	CollectionSeconds   int `json:"collection_seconds"`
	RulesSeconds        int `json:"rules_seconds"`
	ModelSeconds        int `json:"model_seconds"`
	FinalizeSeconds     int `json:"finalize_seconds"`
	ModelRequestSeconds int `json:"model_request_seconds"`
	ModelLogicalCalls   int `json:"model_logical_calls"`
	ModelHTTPAttempts   int `json:"model_http_attempts"`
	ToolCalls           int `json:"tool_calls"`
	ToolBatch           int `json:"tool_batch"`
	CloudHTTPAttempts   int `json:"cloud_http_attempts"`
	AssetsPerBatch      int `json:"assets_per_batch"`
	MaxPolicies         int `json:"max_policies"`
	MaxQueryCells       int `json:"max_query_cells"`
}

// Default documents only the legacy compatibility policy. New explicit profiles
// are independently validated and may exceed these former package constants.
func Default(kind string) Limits {
	switch kind {
	case "audit":
		return Limits{QueueSeconds: 120, ExecutionSeconds: 240, CollectionSeconds: 90, RulesSeconds: 20, ModelSeconds: 120, FinalizeSeconds: 10, ModelRequestSeconds: 30, ModelLogicalCalls: 6, ModelHTTPAttempts: 6, ToolCalls: 12, ToolBatch: 4, CloudHTTPAttempts: 100, AssetsPerBatch: 50, MaxPolicies: 1000, MaxQueryCells: 50000}
	case "assistant_turn":
		return Limits{QueueSeconds: 60, ExecutionSeconds: 90, ModelSeconds: 90, ModelRequestSeconds: 30, ModelLogicalCalls: 4, ModelHTTPAttempts: 4, ToolCalls: 8, ToolBatch: 4, AssetsPerBatch: 50, MaxPolicies: 1000, MaxQueryCells: 50000}
	case "collect":
		return Limits{QueueSeconds: 120, ExecutionSeconds: 90, CollectionSeconds: 90, CloudHTTPAttempts: 100, AssetsPerBatch: 50, MaxPolicies: 1000}
	case "connection_check":
		return Limits{QueueSeconds: 60, ExecutionSeconds: 30, CollectionSeconds: 30, CloudHTTPAttempts: 6}
	case "e0_probe":
		return Limits{QueueSeconds: 120, ExecutionSeconds: 30, CollectionSeconds: 30}
	default:
		return Limits{}
	}
}
func Seconds(value int) (time.Duration, error) {
	if value < 0 || int64(value) > MaxSeconds {
		return 0, ErrInvalid
	}
	return time.Duration(value) * time.Second, nil
}
func publicKind(kind string) bool {
	return kind == "audit" || kind == "assistant_turn" || kind == "collect" || kind == "connection_check"
}
func (l Limits) Validate(kind string) error {
	if !publicKind(kind) && kind != "e0_probe" {
		return ErrInvalid
	}
	for _, v := range []int{l.QueueSeconds, l.ExecutionSeconds, l.CollectionSeconds, l.RulesSeconds, l.ModelSeconds, l.FinalizeSeconds, l.ModelRequestSeconds} {
		if _, err := Seconds(v); err != nil {
			return err
		}
	}
	if l.ExecutionSeconds == 0 || int64(l.QueueSeconds) > MaxSeconds-int64(l.ExecutionSeconds) {
		return ErrInvalid
	}
	remaining := l.ExecutionSeconds
	for _, v := range []int{l.CollectionSeconds, l.RulesSeconds, l.ModelSeconds, l.FinalizeSeconds} {
		if v > remaining {
			return ErrInvalid
		}
		remaining -= v
	}
	for _, v := range []int{l.ModelLogicalCalls, l.ModelHTTPAttempts, l.ToolCalls, l.ToolBatch, l.CloudHTTPAttempts, l.AssetsPerBatch, l.MaxPolicies, l.MaxQueryCells} {
		if v < 0 || v > MaxCounter {
			return ErrInvalid
		}
	}
	if l.ModelSeconds > 0 && l.ModelLogicalCalls > 0 && l.ModelHTTPAttempts > 0 && l.ModelRequestSeconds == 0 {
		return ErrInvalid
	}
	if l.ModelRequestSeconds > l.ModelSeconds || l.ToolBatch > l.ToolCalls {
		return ErrInvalid
	}
	if l.ModelSeconds == 0 && (l.ModelRequestSeconds != 0 || l.ModelLogicalCalls != 0 || l.ModelHTTPAttempts != 0 || l.ToolCalls != 0 || l.ToolBatch != 0) {
		return ErrInvalid
	}
	if kind == "audit" && (l.RulesSeconds == 0 || l.AssetsPerBatch == 0 || l.MaxPolicies == 0 || l.MaxQueryCells == 0) {
		return ErrInvalid
	}
	if kind == "assistant_turn" && (l.ModelSeconds == 0 || l.AssetsPerBatch == 0 || l.MaxPolicies == 0) {
		return ErrInvalid
	}
	if kind == "collect" && (l.CollectionSeconds == 0 || l.AssetsPerBatch == 0 || l.MaxPolicies == 0) {
		return ErrInvalid
	}
	if kind == "connection_check" && l.CollectionSeconds == 0 {
		return ErrInvalid
	}
	if (kind == "collect" || kind == "connection_check" || kind == "e0_probe") && (l.RulesSeconds != 0 || l.ModelSeconds != 0) {
		return ErrInvalid
	}
	if kind == "assistant_turn" && (l.CollectionSeconds != 0 || l.RulesSeconds != 0 || l.CloudHTTPAttempts != 0) {
		return ErrInvalid
	}
	return nil
}

// Ceilings use pointers so omitted restrictions and explicit zero are distinct.
// Model HTTP and logical calls remain separate; applying one never refills the
// other or the shared cumulative ledger.
type Ceilings struct {
	ModelRequestSeconds *int
	ModelLogicalCalls   *int
	ModelHTTPAttempts   *int
	ToolCalls           *int
	ToolBatch           *int
	CloudHTTPAttempts   *int
	AssetsPerBatch      *int
	MaxPolicies         *int
	MaxQueryCells       *int
}

func (l Limits) Constrain(kind string, c Ceilings) (Limits, error) {
	if err := l.Validate(kind); err != nil {
		return Limits{}, err
	}
	pairs := []struct {
		value   *int
		ceiling *int
	}{{&l.ModelRequestSeconds, c.ModelRequestSeconds}, {&l.ModelLogicalCalls, c.ModelLogicalCalls}, {&l.ModelHTTPAttempts, c.ModelHTTPAttempts}, {&l.ToolCalls, c.ToolCalls}, {&l.ToolBatch, c.ToolBatch}, {&l.CloudHTTPAttempts, c.CloudHTTPAttempts}, {&l.AssetsPerBatch, c.AssetsPerBatch}, {&l.MaxPolicies, c.MaxPolicies}, {&l.MaxQueryCells, c.MaxQueryCells}}
	for _, p := range pairs {
		if p.ceiling != nil {
			if *p.ceiling < 0 {
				return Limits{}, ErrInvalid
			}
			if *p.ceiling < *p.value {
				*p.value = *p.ceiling
			}
		}
	}
	if l.ToolBatch > l.ToolCalls {
		l.ToolBatch = l.ToolCalls
	}
	// A zero size restriction blocks the plan instead of becoming a default.
	if err := l.Validate(kind); err != nil {
		return Limits{}, err
	}
	return l, nil
}

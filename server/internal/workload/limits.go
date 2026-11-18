package workload

import (
	"errors"
	"math"
	"time"
)

const MaxCounter = 1_000_000_000
const DefaultProfileRef = "default"
const MaxSeconds = int64(math.MaxInt64) / int64(time.Second)

var ErrInvalid = errors.New("EXECUTION_CONFIG_INVALID")

// Limits has one task deadline, one queue window and one Agent round limit.
// Data limits bound memory and rule computation, rather than provider spending.
type Limits struct {
	QueueSeconds     int `json:"queue_seconds"`
	ExecutionSeconds int `json:"execution_seconds"`
	MaxRounds        int `json:"max_rounds"`
	AssetsPerBatch   int `json:"assets_per_batch"`
	MaxPolicies      int `json:"max_policies"`
	MaxQueryCells    int `json:"max_query_cells"`
}

func Default(kind string) Limits {
	l := Limits{QueueSeconds: 120, ExecutionSeconds: 240, MaxRounds: 6, AssetsPerBatch: 50, MaxPolicies: 1000, MaxQueryCells: 50000}
	if kind == "e0_probe" {
		l.ExecutionSeconds = 30
	}
	return l
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
	if _, e := Seconds(l.QueueSeconds); e != nil {
		return e
	}
	if _, e := Seconds(l.ExecutionSeconds); e != nil {
		return e
	}
	if l.ExecutionSeconds <= 0 || int64(l.QueueSeconds) > MaxSeconds-int64(l.ExecutionSeconds) || l.MaxRounds < 1 || l.MaxRounds == math.MaxInt || l.AssetsPerBatch < 1 || l.MaxPolicies < 1 || l.MaxQueryCells < 1 {
		return ErrInvalid
	}
	return nil
}

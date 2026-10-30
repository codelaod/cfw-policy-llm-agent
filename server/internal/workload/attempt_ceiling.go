package workload

import "time"

// AttemptCeiling is an adapter-specific restriction on the existing shared
// authorization. It never creates a period or grants extra task attempts.
type AttemptCeiling struct {
	Ref                 string
	ApplicationAttempts *int64
	SessionAttempts     *int64
	ExpiresAt           string
}

func (c AttemptCeiling) Validate() error {
	if c.Ref != "" && !alias.MatchString(c.Ref) {
		return ErrInvalid
	}
	for _, v := range []*int64{c.ApplicationAttempts, c.SessionAttempts} {
		if v != nil && (*v < 0 || *v > MaxCounter) {
			return ErrInvalid
		}
	}
	if c.ExpiresAt != "" {
		if _, e := time.Parse(time.RFC3339Nano, c.ExpiresAt); e != nil {
			return ErrInvalid
		}
	}
	return nil
}

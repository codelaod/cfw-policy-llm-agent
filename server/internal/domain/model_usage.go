package domain

// MaxReportedModelTokens is the exact-integer boundary of the public JSON
// representation, not a provider token limit or a monetary authorization.
const MaxReportedModelTokens int64 = 1<<53 - 1

// ModelUsage holds only complete counters explicitly reported by a provider.
// Missing usage is represented by a nil pointer. Reported total is preserved
// independently; the application does not derive a bill or recompute that total.
type ModelUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

func (u ModelUsage) Valid() bool {
	return u.PromptTokens >= 0 && u.PromptTokens <= MaxReportedModelTokens && u.CompletionTokens >= 0 && u.CompletionTokens <= MaxReportedModelTokens && u.TotalTokens >= 0 && u.TotalTokens <= MaxReportedModelTokens
}

package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cfw-policy-llm-agent/server/internal/aliyuncollector"
	"cfw-policy-llm-agent/server/internal/domain"
	"cfw-policy-llm-agent/server/internal/storage"
)

// Cancellation can commit before the worker context callback arrives. A late
// successful response is never admitted as a snapshot after that transition;
// retain only the same failure-observation product as an interrupted read.
func (s *CloudService) collectionPublicationFailure(ctx context.Context, job storage.Job, p storage.ExecutionPlan, scope domain.Scope, collected aliyuncollector.Collection, fallback string) error {
	if ctx.Err() != nil {
		return s.preserveCollectionFailure(ctx, job, p, scope, collected, ctx.Err())
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	current, e := s.Store.Get(readCtx, job.ID)
	if e == nil && current.Status == "cancelling" {
		return s.preserveCollectionFailure(ctx, job, p, scope, collected, context.Canceled)
	}
	return workflowFailure(ctx, fallback, false)
}

func (s *CloudService) preserveCollectionFailure(ctx context.Context, job storage.Job, p storage.ExecutionPlan, scope domain.Scope, collected aliyuncollector.Collection, cause error) error {
	failure := cloudFailure(ctx, cause, "COLLECTION_FAILED")
	code := "COLLECTION_FAILED"
	var controlled JobFailure
	if errors.As(failure, &controlled) {
		code = controlled.Code
	} else if errors.Is(failure, context.Canceled) {
		code = "CANCELLED"
	} else if errors.Is(failure, context.DeadlineExceeded) {
		code = "JOB_DEADLINE_EXCEEDED"
	}
	if code == "CLOUD_RESPONSE_CONTAINS_CREDENTIAL" {
		return failure
	}
	// Reflection is checked before any error-path persistence too. A failed
	// provider response may contain unsafe content and never becomes an artifact.
	for _, artifact := range collected.Artifacts {
		query, e := json.Marshal(artifact.Query)
		if e != nil || cloudContainsSecret(query, s.SensitiveValues) || cloudContainsSecret(artifact.Body, s.SensitiveValues) {
			return workflowFailure(ctx, "CLOUD_RESPONSE_CONTAINS_CREDENTIAL", false)
		}
	}
	content, e := cloudContent(job, p, collected)
	if e != nil {
		return e
	}
	content.Kind, content.ScopeRef, content.ProductReady = job.Kind, scope.Ref, false
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, e = s.Store.SaveCloudFailureObservations(saveCtx, job.ID, scope, content, code); e != nil {
		return workflowFailure(ctx, "COLLECTION_OBSERVATION_SAVE_FAILED", false)
	}
	if errors.As(failure, &controlled) && len(content.Observations) > 0 {
		controlled.Partial = true
		return controlled
	}
	return failure
}

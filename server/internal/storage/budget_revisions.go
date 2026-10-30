package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"
)

type BudgetRevision struct {
	Ref              string       `json:"ref"`
	Revision         int64        `json:"revision"`
	ChangeID         string       `json:"change_id"`
	ActorRef         string       `json:"actor_ref"`
	Reason           string       `json:"reason"`
	AuthorizationRef string       `json:"authorization_ref"`
	CreatedAt        string       `json:"created_at"`
	Policy           BudgetPolicy `json:"policy" gorm:"-"`
	Body             string       `json:"-"`
	RequestDigest    string       `json:"-"`
}
type BudgetChange struct {
	ExpectedBaseRevision     int64   `json:"expected_base_revision"`
	ChangeID                 string  `json:"change_id"`
	ActorRef                 string  `json:"actor_ref"`
	Reason                   string  `json:"reason"`
	AuthorizationRef         string  `json:"authorization_ref"`
	ApplicationCloudAttempts *int64  `json:"application_cloud_attempts,omitempty"`
	ApplicationModelAttempts *int64  `json:"application_model_attempts,omitempty"`
	SessionCloudAttempts     *int64  `json:"session_cloud_attempts,omitempty"`
	SessionModelAttempts     *int64  `json:"session_model_attempts,omitempty"`
	ExpiresAt                *string `json:"expires_at,omitempty"`
}

func validateBudgetPolicy(p BudgetPolicy) (BudgetPolicy, error) {
	if !validKey(p.Ref) || !boundedBudgetLimit(p.ApplicationCloudAttempts) || !boundedBudgetLimit(p.ApplicationModelAttempts) || !boundedBudgetLimit(p.SessionCloudAttempts) || !boundedBudgetLimit(p.SessionModelAttempts) {
		return p, ErrInputInvalid
	}
	if p.ExpiresAt != "" {
		at, e := time.Parse(time.RFC3339Nano, p.ExpiresAt)
		if e != nil {
			return p, ErrInputInvalid
		}
		p.ExpiresAt = at.UTC().Format(time.RFC3339Nano)
	}
	return p, nil
}
func budgetExpanded(old, next BudgetPolicy) bool {
	if next.ApplicationCloudAttempts > old.ApplicationCloudAttempts || next.ApplicationModelAttempts > old.ApplicationModelAttempts || next.SessionCloudAttempts > old.SessionCloudAttempts || next.SessionModelAttempts > old.SessionModelAttempts {
		return true
	}
	if old.ExpiresAt != "" {
		before, e := time.Parse(time.RFC3339Nano, old.ExpiresAt)
		if e != nil {
			return true
		}
		if next.ExpiresAt == "" {
			return true
		}
		after, e := time.Parse(time.RFC3339Nano, next.ExpiresAt)
		return e != nil || after.After(before)
	}
	return false
}
func budgetHead(tx *gorm.DB, ref string) (revision BudgetRevision, err error) {
	r := tx.Raw("SELECT r.* FROM budget_policy_revisions r JOIN budget_policy_heads h ON h.ref=r.ref AND h.revision=r.revision WHERE h.ref=?", ref).Scan(&revision)
	if r.Error != nil {
		return revision, r.Error
	}
	if r.RowsAffected != 1 {
		return revision, ErrBudgetNotConfigured
	}
	err = json.Unmarshal([]byte(revision.Body), &revision.Policy)
	return
}
func appendBudgetRevision(tx *gorm.DB, policy BudgetPolicy, base int64, changeID, digest, actor, reason, authorization string) (out BudgetRevision, err error) {
	body, e := marshalBounded(policy, 16384)
	if e != nil {
		return out, e
	}
	out = BudgetRevision{Ref: policy.Ref, Revision: base + 1, ChangeID: changeID, RequestDigest: digest, ActorRef: actor, Reason: reason, AuthorizationRef: authorization, CreatedAt: now(), Policy: policy, Body: string(body)}
	if e = tx.Exec("INSERT INTO budget_policy_revisions(ref,revision,change_id,request_digest,actor_ref,reason,authorization_ref,created_at,body) VALUES(?,?,?,?,?,?,?,?,?)", out.Ref, out.Revision, changeID, digest, actor, reason, authorization, out.CreatedAt, out.Body).Error; e != nil {
		return out, e
	}
	if base == 0 {
		e = tx.Exec("INSERT INTO budget_policy_heads(ref,revision) VALUES(?,?)", policy.Ref, out.Revision).Error
	} else {
		r := tx.Exec("UPDATE budget_policy_heads SET revision=? WHERE ref=? AND revision=?", out.Revision, policy.Ref, base)
		e = r.Error
		if e == nil && r.RowsAffected != 1 {
			e = ErrVersionConflict
		}
	}
	return out, e
}
func (s *Store) CurrentBudgetRevision(ctx context.Context, ref string) (BudgetRevision, error) {
	return budgetHead(s.db.WithContext(ctx), ref)
}

// ReviseBudgetPolicy is an explicit administrative operation, never called by a
// model, ordinary task submission, credential rotation or automatic retry.
func (s *Store) ReviseBudgetPolicy(ctx context.Context, ref string, change BudgetChange) (out BudgetRevision, replayed bool, err error) {
	if !validKey(ref) || change.ExpectedBaseRevision < 1 || !validKey(change.ChangeID) || !validKey(change.ActorRef) || !validKey(change.AuthorizationRef) || strings.TrimSpace(change.Reason) == "" || len(change.Reason) > 1024 {
		return out, false, ErrInputInvalid
	}
	if change.ApplicationCloudAttempts == nil && change.ApplicationModelAttempts == nil && change.SessionCloudAttempts == nil && change.SessionModelAttempts == nil && change.ExpiresAt == nil {
		return out, false, ErrInputInvalid
	}
	digest := requestDigest(struct {
		Ref    string
		Change BudgetChange
	}{ref, change})
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing BudgetRevision
		r := tx.Raw("SELECT * FROM budget_policy_revisions WHERE change_id=?", change.ChangeID).Scan(&existing)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected > 0 {
			if existing.Ref != ref || existing.RequestDigest != digest {
				return ErrConflict
			}
			out = existing
			replayed = true
			return json.Unmarshal([]byte(out.Body), &out.Policy)
		}
		head, e := budgetHead(tx, ref)
		if e != nil {
			return e
		}
		if head.Revision != change.ExpectedBaseRevision {
			return ErrVersionConflict
		}
		policy, e := budgetPolicy(tx, ref)
		if e != nil {
			return e
		}
		for _, v := range []struct {
			dst   *int64
			value *int64
		}{{&policy.ApplicationCloudAttempts, change.ApplicationCloudAttempts}, {&policy.ApplicationModelAttempts, change.ApplicationModelAttempts}, {&policy.SessionCloudAttempts, change.SessionCloudAttempts}, {&policy.SessionModelAttempts, change.SessionModelAttempts}} {
			if v.value != nil {
				*v.dst = *v.value
			}
		}
		if change.ExpiresAt != nil {
			policy.ExpiresAt = *change.ExpiresAt
		}
		policy, e = validateBudgetPolicy(policy)
		if e != nil {
			return e
		}
		if e = tx.Exec("UPDATE budget_policies SET application_cloud_attempts=?,application_model_attempts=?,session_cloud_attempts=?,session_model_attempts=?,expires_at=? WHERE ref=?", policy.ApplicationCloudAttempts, policy.ApplicationModelAttempts, policy.SessionCloudAttempts, policy.SessionModelAttempts, policy.ExpiresAt, ref).Error; e != nil {
			return e
		}
		out, e = appendBudgetRevision(tx, policy, head.Revision, change.ChangeID, digest, change.ActorRef, change.Reason, change.AuthorizationRef)
		return e
	})
	return
}

func configureBudgetPolicy(tx *gorm.DB, policy BudgetPolicy) error {
	var e error
	policy, e = validateBudgetPolicy(policy)
	if e != nil {
		return e
	}
	old, e := budgetPolicy(tx, policy.Ref)
	if e == ErrBudgetNotConfigured {
		policy.CreatedAt = now()
		if e = tx.Exec("INSERT INTO budget_policies(ref,application_cloud_attempts,application_model_attempts,session_cloud_attempts,session_model_attempts,expires_at,created_at) VALUES(?,?,?,?,?,?,?)", policy.Ref, policy.ApplicationCloudAttempts, policy.ApplicationModelAttempts, policy.SessionCloudAttempts, policy.SessionModelAttempts, policy.ExpiresAt, policy.CreatedAt).Error; e != nil {
			return e
		}
		_, e = appendBudgetRevision(tx, policy, 0, "initial_"+requestDigest(policy.Ref), requestDigest(policy), "explicit-configuration", "initial explicitly selected authorization", "configuration")
		return e
	}
	if e != nil {
		return e
	}
	policy.CreatedAt = old.CreatedAt
	if reflect.DeepEqual(old, policy) {
		return nil
	}
	if budgetExpanded(old, policy) {
		return ErrConflict
	}
	head, e := budgetHead(tx, policy.Ref)
	if e != nil {
		return e
	}
	if e = tx.Exec("UPDATE budget_policies SET application_cloud_attempts=?,application_model_attempts=?,session_cloud_attempts=?,session_model_attempts=?,expires_at=? WHERE ref=?", policy.ApplicationCloudAttempts, policy.ApplicationModelAttempts, policy.SessionCloudAttempts, policy.SessionModelAttempts, policy.ExpiresAt, policy.Ref).Error; e != nil {
		return e
	}
	digest := requestDigest(struct {
		Base   int64
		Policy BudgetPolicy
	}{head.Revision, policy})
	_, e = appendBudgetRevision(tx, policy, head.Revision, "tighten_"+digest, digest, "explicit-configuration", "configuration tightened existing policy", "configuration")
	return e
}

// BindBudgetOwner prevents configuration aliases and key rotation from opening
// a new cumulative ledger for the same model or cloud account.
func (s *Store) BindBudgetOwner(ctx context.Context, owner, ref string) error {
	if !validKey(owner) || !validKey(ref) {
		return ErrInputInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return bindBudgetOwner(tx, owner, ref) })
}
func bindBudgetOwner(tx *gorm.DB, owner, ref string) error {

	if _, e := budgetPolicy(tx, ref); e != nil {
		return e
	}
	var existing string
	if e := tx.Raw("SELECT budget_ref FROM execution_budget_bindings WHERE owner_ref=?", owner).Scan(&existing).Error; e != nil {
		return e
	}
	if existing != "" {
		if existing != ref {
			return ErrConflict
		}
		return nil
	}
	var prior []string
	q := "SELECT DISTINCT budget_ref FROM execution_plans WHERE json_extract(body,'$.effect_ceiling.allow_model_requests')=1"
	args := []any{}
	if owner == "application-root" {
		q = "SELECT DISTINCT budget_ref FROM execution_plans WHERE json_extract(body,'$.effect_ceiling.allow_cloud_reads')=1 OR json_extract(body,'$.effect_ceiling.allow_model_requests')=1"
	}
	if strings.HasPrefix(owner, "cloud-account:") {
		// The approved application has one cloud account. Existing cloud
		// grants cannot be reset by renaming the configured connection.
		q = "SELECT DISTINCT budget_ref FROM execution_plans WHERE json_extract(body,'$.effect_ceiling.allow_cloud_reads')=1"
	}
	if e := tx.Raw(q, args...).Scan(&prior).Error; e != nil {
		return e
	}
	for _, value := range prior {
		if value != ref {
			return ErrConflict
		}
	}
	return tx.Exec("INSERT INTO execution_budget_bindings(owner_ref,budget_ref) VALUES(?,?)", owner, ref).Error
}
func (s *Store) ConfigureBudgetForOwners(ctx context.Context, policy BudgetPolicy, owners []string) error {
	if len(owners) > 3 {
		return ErrInputInvalid
	}
	for _, owner := range owners {
		if !validKey(owner) {
			return ErrInputInvalid
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := configureBudgetPolicy(tx, policy); e != nil {
			return e
		}
		if e := bindBudgetOwner(tx, "application-root", policy.Ref); e != nil {
			return e
		}
		for _, owner := range owners {
			if e := bindBudgetOwner(tx, owner, policy.Ref); e != nil {
				return e
			}
		}
		return nil
	})
}

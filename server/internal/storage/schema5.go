package storage

import (
	"encoding/json"
	"errors"

	"gorm.io/gorm"
)

var workflowColumns = map[string][]string{
	"profile_draft_rebase_requests":  {"request_key", "draft_id", "request_digest", "result_json"},
	"execution_budget_bindings":      {"owner_ref", "budget_ref"},
	"budget_policy_revisions":        {"ref", "revision", "change_id", "request_digest", "actor_ref", "reason", "authorization_ref", "created_at", "body"},
	"budget_policy_heads":            {"ref", "revision"},
	"resource_catalog":               {"id", "source_ref", "account_ref", "region", "resource_type", "resource_id", "name", "body", "observed_at"},
	"resource_snapshot_bindings":     {"resource_ref", "snapshot_id", "asset_id", "scope_ref", "boundary", "direction"},
	"resource_catalogue_state":       {"id", "revision"},
	"resource_catalogue_coverage":    {"id", "source_ref", "body"},
	"conversation_intents":           {"id", "conversation_id", "revision", "body", "created_at"},
	"conversation_intent_heads":      {"conversation_id", "intent_id", "revision"},
	"conversation_intent_requests":   {"request_key", "conversation_id", "request_digest", "result_json"},
	"conversation_creation_requests": {"request_key", "request_digest", "conversation_id"},
	"action_proposals":               {"id", "conversation_id", "intent_id", "intent_revision", "body", "status", "plan_id"},
	"audit_discovery":                {"id", "job_id", "created_at", "source_ref", "config_revision", "body", "raw_artifacts", "request_digest"},
	"conversation_business_drafts":   {"conversation_id", "target_ref", "draft_id"},
	"conversation_business_requests": {"request_key", "conversation_id", "request_digest", "result_json"},
	"conversation_role_decisions":    {"id", "conversation_id", "target_ref", "candidate_ref", "decision", "request_key", "created_at"},
}

func checkWorkflowShape(db *gorm.DB) error {
	for table, columns := range workflowColumns {
		var names []struct{ Name string }
		if err := db.Raw("SELECT name FROM pragma_table_info(?)", table).Scan(&names).Error; err != nil {
			return err
		}
		found := map[string]bool{}
		for _, n := range names {
			found[n.Name] = true
		}
		for _, c := range columns {
			if !found[c] {
				return errors.New("customer workflow schema incomplete; explicit migration required")
			}
		}
	}
	var singleton int64
	if err := db.Raw("SELECT count(*) FROM resource_catalogue_state WHERE id=1 AND revision>=0").Scan(&singleton).Error; err != nil {
		return err
	}
	if singleton != 1 {
		return errors.New("resource catalogue revision missing; explicit migration required")
	}
	var orphan int64
	if err := db.Raw("SELECT count(*) FROM budget_policies p LEFT JOIN budget_policy_heads h ON h.ref=p.ref LEFT JOIN budget_policy_revisions r ON r.ref=h.ref AND r.revision=h.revision WHERE r.ref IS NULL").Scan(&orphan).Error; err != nil {
		return err
	}
	if orphan != 0 {
		return errors.New("authorization revision head missing; explicit migration required")
	}
	if err := checkBatchSchema(db); err != nil {
		return err
	}
	if err := checkResourceGroupSchema(db); err != nil {
		return err
	}
	return checkPreparedAuditSchema(db)
}
func migrateWorkflowSchema(tx *gorm.DB) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS profile_draft_rebase_requests(request_key TEXT PRIMARY KEY,draft_id TEXT NOT NULL REFERENCES profile_drafts(id),request_digest TEXT NOT NULL,result_json TEXT NOT NULL CHECK(json_valid(result_json)))`,
		`CREATE TABLE IF NOT EXISTS execution_budget_bindings(owner_ref TEXT PRIMARY KEY,budget_ref TEXT NOT NULL REFERENCES budget_policies(ref))`,

		`CREATE TABLE IF NOT EXISTS budget_policy_revisions(ref TEXT NOT NULL REFERENCES budget_policies(ref),revision INTEGER NOT NULL CHECK(revision>0),change_id TEXT UNIQUE NOT NULL,request_digest TEXT NOT NULL,actor_ref TEXT NOT NULL,reason TEXT NOT NULL,authorization_ref TEXT NOT NULL,created_at TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),PRIMARY KEY(ref,revision))`,
		`CREATE TABLE IF NOT EXISTS budget_policy_heads(ref TEXT PRIMARY KEY REFERENCES budget_policies(ref),revision INTEGER NOT NULL,FOREIGN KEY(ref,revision) REFERENCES budget_policy_revisions(ref,revision))`,

		`CREATE TABLE IF NOT EXISTS resource_catalog(id TEXT PRIMARY KEY,source_ref TEXT NOT NULL,account_ref TEXT NOT NULL,region TEXT NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,name TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),observed_at TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS resource_search ON resource_catalog(account_ref,region,resource_type,name,id)`,
		`CREATE INDEX IF NOT EXISTS resource_provider_id ON resource_catalog(resource_id,id)`,
		`CREATE TABLE IF NOT EXISTS resource_snapshot_bindings(resource_ref TEXT NOT NULL REFERENCES resource_catalog(id),snapshot_id TEXT NOT NULL REFERENCES snapshots(id),asset_id TEXT NOT NULL,scope_ref TEXT NOT NULL,boundary TEXT NOT NULL,direction TEXT NOT NULL,PRIMARY KEY(resource_ref,snapshot_id,asset_id))`,
		`CREATE INDEX IF NOT EXISTS resource_snapshot_scope ON resource_snapshot_bindings(scope_ref,resource_ref,snapshot_id)`,
		`CREATE TABLE IF NOT EXISTS resource_catalogue_state(id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL CHECK(revision>=0))`,
		`INSERT OR IGNORE INTO resource_catalogue_state VALUES(1,0)`,
		`CREATE TABLE IF NOT EXISTS resource_catalogue_coverage(id TEXT PRIMARY KEY,source_ref TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)))`,
		`CREATE TABLE IF NOT EXISTS conversation_intents(id TEXT NOT NULL,conversation_id TEXT NOT NULL REFERENCES conversations(id),revision INTEGER NOT NULL CHECK(revision>0),body TEXT NOT NULL CHECK(json_valid(body)),created_at TEXT NOT NULL,PRIMARY KEY(id,revision))`,
		`CREATE TABLE IF NOT EXISTS conversation_intent_heads(conversation_id TEXT PRIMARY KEY REFERENCES conversations(id),intent_id TEXT NOT NULL,revision INTEGER NOT NULL,FOREIGN KEY(intent_id,revision) REFERENCES conversation_intents(id,revision))`,
		`CREATE TABLE IF NOT EXISTS conversation_intent_requests(request_key TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),request_digest TEXT NOT NULL,result_json TEXT NOT NULL CHECK(json_valid(result_json)))`,
		`CREATE TABLE IF NOT EXISTS conversation_creation_requests(request_key TEXT PRIMARY KEY,request_digest TEXT NOT NULL,conversation_id TEXT NOT NULL REFERENCES conversations(id))`,
		`CREATE TABLE IF NOT EXISTS action_proposals(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),intent_id TEXT NOT NULL,intent_revision INTEGER NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),status TEXT NOT NULL,plan_id TEXT,FOREIGN KEY(intent_id,intent_revision) REFERENCES conversation_intents(id,revision))`,
		`CREATE TABLE IF NOT EXISTS audit_discovery(id TEXT PRIMARY KEY,job_id TEXT UNIQUE NOT NULL REFERENCES e0_jobs(id),created_at TEXT NOT NULL,source_ref TEXT NOT NULL,config_revision TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body)),raw_artifacts TEXT NOT NULL CHECK(json_valid(raw_artifacts)),request_digest TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS conversation_business_drafts(conversation_id TEXT NOT NULL REFERENCES conversations(id),target_ref TEXT NOT NULL,draft_id TEXT UNIQUE NOT NULL REFERENCES profile_drafts(id),PRIMARY KEY(conversation_id,target_ref))`,
		`CREATE TABLE IF NOT EXISTS conversation_business_requests(request_key TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),request_digest TEXT NOT NULL,result_json TEXT NOT NULL CHECK(json_valid(result_json)))`,
		`CREATE TABLE IF NOT EXISTS conversation_role_decisions(id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL REFERENCES conversations(id),target_ref TEXT NOT NULL,candidate_ref TEXT NOT NULL,decision TEXT NOT NULL CHECK(decision IN ('accepted_for_preview','rejected','unknown')),request_key TEXT UNIQUE NOT NULL,created_at TEXT NOT NULL)`,
	} {
		if err := tx.Exec(q).Error; err != nil {
			return err
		}
	}
	var policies []BudgetPolicy
	if err := tx.Raw("SELECT p.* FROM budget_policies p WHERE NOT EXISTS(SELECT 1 FROM budget_policy_heads h WHERE h.ref=p.ref)").Scan(&policies).Error; err != nil {
		return err
	}
	for _, p := range policies {
		if _, err := appendBudgetRevision(tx, p, 0, "migration_"+requestDigest(p.Ref), requestDigest(p), "schema-migration", "preserve existing policy and attempts", "explicit-migration"); err != nil {
			return err
		}
	}
	var version int
	if err := tx.Raw("SELECT version FROM business_schema").Scan(&version).Error; err != nil {
		return err
	}
	if version != 4 && version != 5 {
		return errors.New("unsupported workflow migration")
	}
	if version == 4 {
		var rows []SnapshotRecord
		if err := tx.Raw("SELECT * FROM snapshots ORDER BY created_at,id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := json.Unmarshal([]byte(row.Body), &row.Input); err != nil {
				return err
			}
			if err := indexSnapshotResources(tx, row); err != nil {
				return err
			}
		}
		for _, q := range []string{"DROP TABLE business_schema", "CREATE TABLE business_schema(version INTEGER PRIMARY KEY CHECK(version=5))", "INSERT INTO business_schema VALUES(5)"} {
			if err := tx.Exec(q).Error; err != nil {
				return err
			}
		}
	}
	if err := migrateBatchSchema(tx); err != nil {
		return err
	}
	if err := migrateResourceGroupSchema(tx); err != nil {
		return err
	}
	if err := migratePreparedAuditSchema(tx); err != nil {
		return err
	}
	return checkWorkflowShape(tx)
}

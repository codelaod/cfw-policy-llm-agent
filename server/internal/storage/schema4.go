package storage

import (
	"errors"

	"gorm.io/gorm"
)

func checkCloudShape(db *gorm.DB) error {
	for table, columns := range map[string][]string{
		"budget_policies":          {"ref", "application_cloud_attempts", "application_model_attempts", "session_cloud_attempts", "session_model_attempts", "expires_at", "created_at"},
		"outbound_attempts":        {"id", "job_id", "conversation_id", "budget_ref", "class", "bucket", "source_ref", "state", "error_code", "http_status", "duration_ms", "reserved_at", "finished_at", "usage_json"},
		"job_agent_results":        {"id", "job_id", "created_at", "body"},
		"configured_scope_catalog": {"scope_ref", "source_ref", "config_revision", "body", "active"},
		"job_cloud_results":        {"id", "job_id", "created_at", "source_ref", "config_revision", "body", "raw_artifacts", "request_digest"},
	} {
		var names []struct{ Name string }
		if err := db.Raw("SELECT name FROM pragma_table_info(?)", table).Scan(&names).Error; err != nil {
			return err
		}
		present := map[string]bool{}
		for _, name := range names {
			present[name.Name] = true
		}
		for _, column := range columns {
			if !present[column] {
				return errors.New("cloud schema is incomplete; explicit migration required")
			}
		}
	}
	return nil
}

func migrateCloudSchema(tx *gorm.DB) error {
	var version int
	if err := tx.Raw("SELECT version FROM business_schema").Scan(&version).Error; err != nil {
		return err
	}
	if version != 3 && version != 4 {
		return errors.New("unsupported business schema for cloud migration")
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS configured_scope_catalog(scope_ref TEXT PRIMARY KEY,source_ref TEXT NOT NULL,config_revision TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body) AND length(CAST(body AS BLOB))<=4096),active INTEGER NOT NULL CHECK(active IN (0,1)))`,
		`CREATE TABLE IF NOT EXISTS job_cloud_results(id TEXT PRIMARY KEY,job_id TEXT UNIQUE NOT NULL REFERENCES e0_jobs(id),created_at TEXT NOT NULL,source_ref TEXT NOT NULL,config_revision TEXT NOT NULL,body TEXT NOT NULL CHECK(json_valid(body) AND length(CAST(body AS BLOB))<=65536),raw_artifacts TEXT NOT NULL CHECK(json_valid(raw_artifacts) AND length(CAST(raw_artifacts AS BLOB))<=33554432),request_digest TEXT NOT NULL)`,
	} {
		if err := tx.Exec(q).Error; err != nil {
			return err
		}
	}
	if err := checkCloudShape(tx); err != nil {
		return err
	}
	if version == 4 {
		return nil
	}
	for _, q := range []string{"DROP TABLE business_schema", "CREATE TABLE business_schema(version INTEGER PRIMARY KEY CHECK(version=4))", "INSERT INTO business_schema VALUES(4)"} {
		if err := tx.Exec(q).Error; err != nil {
			return err
		}
	}
	return nil
}

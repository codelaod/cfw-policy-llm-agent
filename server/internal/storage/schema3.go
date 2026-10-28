package storage

import (
	"errors"
	"gorm.io/gorm"
)

const BusinessSchemaVersion = 4

func checkAdmissionShape(db *gorm.DB) error {
	var shape int
	if err := db.Raw(`SELECT (SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('budget_policies','outbound_attempts','job_agent_results')) + (SELECT count(*) FROM pragma_table_info('outbound_attempts') WHERE name='usage_json')`).Scan(&shape).Error; err != nil {
		return err
	}
	if shape != 4 {
		return errors.New("admission schema is incomplete; explicit migration required")
	}
	return checkCloudShape(db)
}

func migrateAdmissionSchema(tx *gorm.DB) error {
	var version int
	if err := tx.Raw("SELECT version FROM business_schema").Scan(&version).Error; err != nil {
		return err
	}
	if version != 2 && version != 3 && version != BusinessSchemaVersion {
		return errors.New("unsupported business schema for admission migration")
	}
	if err := migrateBudgets(tx); err != nil {
		return err
	}
	if err := migrateAgentResults(tx); err != nil {
		return err
	}
	if version == 2 {
		for _, sql := range []string{"DROP TABLE business_schema", "CREATE TABLE business_schema(version INTEGER PRIMARY KEY CHECK(version=3))", "INSERT INTO business_schema VALUES(3)"} {
			if err := tx.Exec(sql).Error; err != nil {
				return err
			}
		}
	}
	return migrateCloudSchema(tx)
}

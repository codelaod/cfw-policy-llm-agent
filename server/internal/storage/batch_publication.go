package storage

import (
	"gorm.io/gorm"
	"time"
)

// Normal child publications require the original batch to remain open.
func batchPublicationOpen(tx *gorm.DB, job Job) error {
	if job.ParentJobID == "" {
		return nil
	}
	root, e := batchAuthority(tx, job)
	if e != nil {
		return e
	}
	if root.Status != "running" {
		return ErrJobTerminal
	}
	deadline, e := earlierBatchDeadline(root)
	if e != nil || !time.Now().UTC().Before(deadline) {
		return ErrJobTerminal
	}
	return nil
}

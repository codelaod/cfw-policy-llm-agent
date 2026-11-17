package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
)

var errExecutionConfiguration = errors.New("EXECUTION_CONFIGURATION_INVALID")

type budgetChangeFile struct {
	SchemaVersion int                  `json:"schema_version"`
	BudgetRef     string               `json:"budget_ref"`
	Change        storage.BudgetChange `json:"change"`
}

func readBudgetChange(path string) (request budgetChangeFile, err error) {
	before, e := os.Stat(path)
	if e != nil || !before.Mode().IsRegular() {
		return request, errExecutionConfiguration
	}
	f, e := os.Open(path)
	if e != nil {
		return request, errExecutionConfiguration
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return request, errExecutionConfiguration
	}
	data, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(data) > 65536 || !workload.ValidJSONDocument(data) {
		return request, errExecutionConfiguration
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || request.SchemaVersion != 1 {
		return request, errExecutionConfiguration
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return request, errExecutionConfiguration
	}
	return
}
func applyBudgetChange(dbPath, changePath string, w io.Writer) error {
	request, e := readBudgetChange(changePath)
	if e != nil {
		return e
	}
	store, e := storage.Open(dbPath, false)
	if e != nil {
		return errors.New("BUDGET_LEDGER_UNAVAILABLE")
	}
	defer store.Close()
	revision, replayed, e := store.ReviseBudgetPolicy(context.Background(), request.BudgetRef, request.Change)
	if e != nil {
		return e
	}
	return json.NewEncoder(w).Encode(struct {
		Ref      string `json:"budget_ref"`
		Revision int64  `json:"revision"`
		Replayed bool   `json:"replayed"`
	}{revision.Ref, revision.Revision, replayed})
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/storage"
	"cfw-policy-llm-agent/server/internal/workload"
)

var errExecutionConfiguration = errors.New("EXECUTION_CONFIGURATION_INVALID")

type selectedExecutionSettings struct {
	Model     modelconfig.Config
	Cloud     cloudconfig.Config
	Execution workload.Config
}

func loadExecutionSettings(modelPath, cloudPath, envFile, executionPath string) (out selectedExecutionSettings, err error) {
	out.Model, err = modelconfig.LoadSelected(modelPath, envFile)
	if err != nil {
		return out, err
	}
	if cloudPath != "" {
		out.Cloud, err = cloudconfig.Load(cloudPath)
		if err != nil {
			return out, err
		}
	}
	if executionPath != "" {
		out.Execution, err = workload.Load(executionPath)
		if err != nil {
			return out, err
		}
	}
	out.Model, out.Cloud, err = app.BindExecutionSettings(out.Model, out.Cloud, out.Execution)
	return
}
func checkExecutionConfiguration(modelPath, cloudPath, envFile, executionPath string, w io.Writer) error {
	if executionPath == "" {
		return checkConfigurationSelected(modelPath, cloudPath, envFile, w)
	}
	settings, e := loadExecutionSettings(modelPath, cloudPath, envFile, executionPath)
	if e != nil {
		return e
	}
	return json.NewEncoder(w).Encode(struct {
		Model     modelconfig.Summary `json:"model"`
		Cloud     cloudconfig.Summary `json:"cloud"`
		Execution workload.Summary    `json:"execution"`
	}{settings.Model.Summary(), settings.Cloud.Summary(), settings.Execution.Summary()})
}

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

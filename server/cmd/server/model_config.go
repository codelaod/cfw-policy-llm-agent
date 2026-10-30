package main

import (
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"encoding/json"
	"errors"
	"io"
)

func checkConfiguration(modelPath, cloudPath string, writer io.Writer) error {
	return checkConfigurationSelected(modelPath, cloudPath, "", writer)
}

func checkConfigurationSelected(modelPath, cloudPath, envFile string, writer io.Writer) error {
	if modelPath != "" && envFile != "" {
		return modelconfig.ErrSourceConflict
	}
	if cloudPath == "" {
		cfg, err := modelconfig.LoadSelected(modelPath, envFile)
		if err != nil {
			return err
		}
		return json.NewEncoder(writer).Encode(cfg.Summary())
	}
	cloud, err := cloudconfig.Load(cloudPath)
	if err != nil {
		return err
	}
	result := struct {
		Cloud cloudconfig.Summary  `json:"cloud"`
		Model *modelconfig.Summary `json:"model,omitempty"`
	}{Cloud: cloud.Summary()}
	if modelPath != "" || envFile != "" {
		model, err := modelconfig.LoadSelected(modelPath, envFile)
		if err != nil {
			return err
		}
		if model.Enabled() && cloud.Enabled() && model.Budget().Ref != cloud.Budget().Ref {
			return errors.New("CLOUD_MODEL_BUDGET_MISMATCH")
		}
		summary := model.Summary()
		result.Model = &summary
	}
	return json.NewEncoder(writer).Encode(result)
}

func checkModelConfig(path string, writer io.Writer) error {
	cfg, err := modelconfig.Load(path)
	if err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(cfg.Summary())
}

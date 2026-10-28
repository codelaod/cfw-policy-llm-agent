package main

import (
	"cfw-policy-llm-agent/server/internal/cloudconfig"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"encoding/json"
	"errors"
	"io"
)

func checkConfiguration(modelPath, cloudPath string, writer io.Writer) error {
	if cloudPath == "" {
		return checkModelConfig(modelPath, writer)
	}
	cloud, err := cloudconfig.Load(cloudPath)
	if err != nil {
		return err
	}
	result := struct {
		Cloud cloudconfig.Summary  `json:"cloud"`
		Model *modelconfig.Summary `json:"model,omitempty"`
	}{Cloud: cloud.Summary()}
	if modelPath != "" {
		model, err := modelconfig.Load(modelPath)
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

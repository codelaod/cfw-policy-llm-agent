package main

import (
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"encoding/json"
	"io"
)

func checkModelConfig(path string, writer io.Writer) error {
	cfg, err := modelconfig.Load(path)
	if err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(cfg.Summary())
}

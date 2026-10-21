package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	scenario := flag.String("scenario", "all", "all, whitelist or missing-whitelist (synthetic fixtures only)")
	configCheck := flag.Bool("check-model-config", false, "validate private model configuration without sending requests")
	envFile := flag.String("env-file", ".env.local", "private model config file; only read with -check-model-config")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if *configCheck {
		os.Exit(checkModelConfiguration(*envFile, os.LookupEnv, os.Stdout, os.Stderr))
	}
	scenarios := []string{*scenario}
	if *scenario == "all" {
		scenarios = []string{"whitelist", "missing-whitelist"}
	}
	var reports []Report
	failed := false
	for _, name := range scenarios {
		opts := defaults()
		opts.Scenario = name
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		report, err := runProbe(ctx, opts)
		cancel()
		if err != nil {
			failed = true
			if report.Mode == "" {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
		}
		reports = append(reports, report)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(reports); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if failed {
		os.Exit(1)
	}
}

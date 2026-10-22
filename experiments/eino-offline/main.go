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
	live := flag.Bool("live", false, "use the configured model with synthetic fixtures; requires explicit request permission")
	allowPaid := flag.Bool("allow-paid-requests", false, "explicitly allow bounded requests to the configured provider")
	provider := flag.String("provider", "openai-compatible", "openai-compatible or minimax")
	configCheck := flag.Bool("check-model-config", false, "validate private model configuration without sending requests")
	envFile := flag.String("env-file", ".env.local", "private model config file; only read with -check-model-config")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if *configCheck && *live {
		fmt.Fprintln(os.Stderr, "configuration check and live mode are mutually exclusive")
		os.Exit(2)
	}
	if *live && !*allowPaid {
		fmt.Fprintln(os.Stderr, "live requests require -allow-paid-requests; filling configuration alone is not permission")
		os.Exit(2)
	}
	if *configCheck {
		os.Exit(checkModelConfiguration(*envFile, os.LookupEnv, os.Stdout, os.Stderr))
	}
	if *live {
		config, err := loadModelConfig(*envFile, os.LookupEnv)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.timeoutSeconds*config.maxCalls)*time.Second)
		report, runErr := runLiveProbe(ctx, config, *scenario, *provider)
		cancel()
		if runErr != nil && report.Mode == "" {
			fmt.Fprintln(os.Stderr, sanitizedLiveError(runErr))
			os.Exit(2)
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if encoder.Encode([]Report{report}) != nil {
			fmt.Fprintln(os.Stderr, "cannot encode sanitized report")
			os.Exit(1)
		}
		if runErr != nil {
			os.Exit(1)
		}
		return
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

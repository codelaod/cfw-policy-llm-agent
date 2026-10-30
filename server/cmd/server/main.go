// Local audit workspace with persistent collaboration and bounded read-only jobs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cfw-policy-llm-agent/server/internal/app"
	"cfw-policy-llm-agent/server/internal/httpapi"
	"cfw-policy-llm-agent/server/internal/modelconfig"
	"cfw-policy-llm-agent/server/internal/storage"
)

// @title CFW audit workspace
// @version 0.4.0
// @description Persistent fixed-input audits, bounded Eino supplements, explicit Alibaba Cloud read-only collection, confirmations and reports with shared attempt accounting. Provider requests require explicit configuration and task acceptance; previews and config-check remain local. Real provider capability remains separately verified.
// @BasePath /api/v1
// @securityDefinitions.apikey AppToken
// @in header
// @name X-App-Token
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	db := flag.String("db", "../data/private/e0.sqlite", "private business database path")
	migrate := flag.Bool("migrate", false, "explicitly apply the local SQL schema and exit")
	webDir := flag.String("web-dir", "", "optional built web/dist directory; serves only public bundle files and business routes")
	envFile := flag.String("env-file", "", "explicit project model environment file; mutually exclusive with model-config")
	modelPath := flag.String("model-config", "", "explicit private model JSON configuration; no dotenv or profile discovery")
	cloudPath := flag.String("cloud-config", "", "explicit private read-only cloud connection JSON; no default credential discovery")
	executionPath := flag.String("execution-config", "", "explicit workload and shared policy JSON; no provider calls")
	budgetChangePath := flag.String("budget-change", "", "apply one explicit local authorization revision and exit; existing ledger required")
	configCheck := flag.Bool("config-check", false, "validate the explicitly selected model configuration locally and print a redacted summary")
	flag.Parse()
	if *budgetChangePath != "" {
		if *configCheck || *migrate || *modelPath != "" || *cloudPath != "" || *envFile != "" || *executionPath != "" {
			return errExecutionConfiguration
		}
		return applyBudgetChange(*db, *budgetChangePath, os.Stdout)
	}
	if *configCheck {
		return checkExecutionConfiguration(*modelPath, *cloudPath, *envFile, *executionPath, os.Stdout)
	}
	if *modelPath != "" && *envFile != "" {
		return modelconfig.ErrSourceConflict
	}
	// Startup/config validation is local and only explicitly selected credentials
	// are read. No provider, identity, dotenv or machine profile is contacted.
	token := os.Getenv("CFW_APP_TOKEN")
	if !*migrate && len(token) < 16 {
		return fmt.Errorf("set CFW_APP_TOKEN to at least 16 characters (value is never logged)")
	}
	store, err := storage.Open(*db, *migrate)
	if err != nil {
		return fmt.Errorf("local storage initialization failed: %w", err)
	}
	defer store.Close()
	if *migrate {
		fmt.Printf("Local business schema version %d ready\n", storage.BusinessSchemaVersion)
		return nil
	}
	if err = store.Recover(context.Background()); err != nil {
		return fmt.Errorf("task recovery failed")
	}
	settings, err := loadExecutionSettings(*modelPath, *cloudPath, *envFile, *executionPath)
	if err != nil {
		return fmt.Errorf("explicit settings are unavailable: %w", err)
	}
	selectedModel := settings.Model
	modelRuntime, err := app.NewRuntimeWithExecution(store, settings.Model, settings.Cloud, settings.Execution, token)
	if err != nil {
		return fmt.Errorf("runtime initialization failed; check explicit settings and the persisted shared authorization")
	}
	defer modelRuntime.Close()
	// A process-owned loopback endpoint supplies bounded artificial work. No user
	// supplied URL, proxy environment, credential, retry, or real model is involved.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1200 * time.Millisecond):
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"source":"synthetic","status":"ok"}`)
	}))
	defer mock.Close()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	workflow := app.NewWorkflow(store)
	workflow.Cloud = modelRuntime.Cloud
	if modelRuntime.Factory != nil {
		workflow.Agent = app.NewAgentService(store, modelRuntime.Factory, modelRuntime.Plans.ConfigRevision, modelRuntime.SensitiveValues)
		workflow.Agent.ModelTimeout = selectedModel.Timeout()
	}
	engine := app.New(store, func(ctx context.Context, j storage.Job) error {
		if j.Kind != "e0_probe" {
			return workflow.Run(ctx, j)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, mock.URL, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("local mock failed")
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 4097))
		if err != nil || len(body) > 4096 || res.StatusCode != 200 || !json.Valid(body) {
			return fmt.Errorf("local mock protocol failed")
		}
		return nil
	})
	privateValues := append([]string{token}, modelRuntime.SensitiveValues...)
	if workflow.Agent != nil {
		workflow.Agent.SensitiveValues = privateValues
	}
	router, err := httpapi.New(engine, token, httpapi.Options{WebDir: *webDir, Plans: modelRuntime.Plans, SensitiveValues: privateValues})
	if err != nil {
		return err
	}
	engine.Start()
	defer engine.Stop()
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errors := make(chan error, 1)
	go func() { errors <- server.ListenAndServe() }()
	fmt.Println("Local audit API listening on 127.0.0.1:8080; web development entry is port 5173")
	select {
	case err = <-errors:
		if err != http.ErrServerClosed {
			return fmt.Errorf("local server failed: %w", err)
		}
	case <-ctx.Done():
	}
	engine.Stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = server.Shutdown(shutdown); err != nil {
		server.Close()
	}
	if err = store.Recover(context.Background()); err != nil {
		return fmt.Errorf("stop state could not be persisted")
	}
	return nil
}

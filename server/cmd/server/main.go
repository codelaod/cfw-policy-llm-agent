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
	"cfw-policy-llm-agent/server/internal/configuration"
	"cfw-policy-llm-agent/server/internal/httpapi"
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

func run() error { return runCommand(os.Args[1:], os.Stdout) }

func runCommand(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("cfw", flag.ContinueOnError)
	flags.SetOutput(output)
	envFile := flags.String("env-file", "", "explicit unified dotenv file; process CFW_* values take precedence")
	migrate := flags.Bool("migrate", false, "explicitly apply the local SQL schema and exit")
	configCheck := flags.Bool("config-check", false, "validate unified settings locally and print a redacted summary")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*configCheck && *migrate) {
		return fmt.Errorf("invalid command options")
	}
	settings, err := configuration.Load(*envFile)
	if err != nil {
		return err
	}
	if *configCheck {
		return json.NewEncoder(output).Encode(settings.Summary())
	}
	token := settings.Token()
	if !*migrate && !settings.TokenReady() {
		return fmt.Errorf("CFW_APP_TOKEN must contain 16..8192 characters without surrounding whitespace (value is never logged)")
	}
	store, err := storage.Open(settings.DBPath(), *migrate)
	if err != nil {
		return fmt.Errorf("local storage initialization failed: %w", err)
	}
	defer store.Close()
	if *migrate {
		fmt.Fprintf(output, "Local business schema version %d ready\n", storage.BusinessSchemaVersion)
		return nil
	}
	if err = store.Recover(context.Background()); err != nil {
		return fmt.Errorf("task recovery failed")
	}
	selectedModel := settings.Model
	modelRuntime, err := app.NewRuntimeWithExecution(store, settings.Model, settings.Cloud, settings.Execution, token)
	if err != nil {
		return fmt.Errorf("runtime initialization failed; check explicit settings")
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
	router, err := httpapi.New(engine, token, httpapi.Options{WebDir: settings.WebDir(), Plans: modelRuntime.Plans, SensitiveValues: privateValues})
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

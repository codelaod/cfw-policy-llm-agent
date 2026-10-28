// Local offline audit service with persistent collaboration and bounded jobs.
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
	"cfw-policy-llm-agent/server/internal/storage"
)

// @title CFW offline audit workspace
// @version 0.2.0
// @description Persistent offline rule audits, confirmations, reports and task observation. Cloud and model adapters are not configured.
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
	flag.Parse()
	// Startup/config validation is local; no dotenv, cloud, or model credential reads.
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
		fmt.Println("Local business schema version 2 ready")
		return nil
	}
	if err = store.Recover(context.Background()); err != nil {
		return fmt.Errorf("task recovery failed")
	}
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
	router, err := httpapi.New(engine, token, httpapi.Options{WebDir: *webDir})
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
	fmt.Println("Offline audit API listening on 127.0.0.1:8080; web development entry is port 5173")
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

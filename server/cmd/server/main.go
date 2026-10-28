// E0 server is a local synthetic dependency/transport probe, not an audit system.
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

// @title CFW E0 local contract probe
// @version 0.1.0
// @description Synthetic E0 endpoints only; no cloud/model integration or business audit.
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
	db := flag.String("db", "../data/private/e0.sqlite", "private E0 database path")
	migrate := flag.Bool("migrate", false, "explicitly apply E0 SQL schema and exit")
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
		fmt.Println("E0 schema version 1 ready")
		return nil
	}
	if err = store.Recover(context.Background()); err != nil {
		return fmt.Errorf("E0 recovery failed")
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
	engine := app.New(store, func(ctx context.Context, j storage.Job) error {
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
	router, err := httpapi.New(engine, token)
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
	fmt.Println("E0 synthetic API listening on 127.0.0.1:8080; web development entry is port 5173")
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
		return fmt.Errorf("E0 stop state could not be persisted")
	}
	return nil
}

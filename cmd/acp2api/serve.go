package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/quonaro/acp2api/internal/agent"
	"github.com/quonaro/acp2api/internal/agent/devin"
	"github.com/quonaro/acp2api/internal/client"
	"github.com/quonaro/acp2api/internal/config"
	"github.com/quonaro/acp2api/internal/handler"
	"github.com/quonaro/acp2api/internal/logger"
	"github.com/quonaro/acp2api/internal/session"
	"github.com/quonaro/acp2api/internal/version"
	"github.com/quonaro/lota/engine"
)

// runServer is the native handler for `acp2api serve`.
func runServer(ctx context.Context, nctx engine.NativeContext) error {
	log := logger.New(nctx.Args["verbose"] == "true")
	slog.SetDefault(log)

	cfg, err := config.Load(nctx.Args["config"])
	if err != nil {
		return err
	}
	if addr := nctx.Args["addr"]; addr != "" {
		cfg.Addr = addr
	}
	if workspace := nctx.Args["workspace"]; workspace != "" {
		cfg.Workspace = workspace
	}
	if permission := nctx.Args["permission"]; permission != "" {
		cfg.Permission = permission
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	registry, err := cfg.BuildRegistry(builtinModules(), agent.OS())
	if err != nil {
		return err
	}
	policy, err := client.ParsePolicy(cfg.Permission)
	if err != nil {
		return err
	}

	workspaceDir := cfg.Workspace
	if workspaceDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workspaceDir = wd
		}
	}

	manager, err := session.New(registry, session.Options{
		Workspace:      workspaceDir,
		Policy:         policy,
		RequestTimeout: cfg.RequestTimeout(),
		SessionTTL:     cfg.SessionTTL(),
	})
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close() }()

	server := handler.New(manager, handler.Options{Token: cfg.Token, Logger: log})

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a single agent turn can legitimately run for minutes,
		// and cutting the connection mid-turn would strand the agent process.
		IdleTimeout: 120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.DiscoverModels {
		// Background, and best-effort: failing to enumerate models is not a
		// reason to refuse the requests that do not name one.
		go manager.Discover(ctx)
	}

	serveErr := make(chan error, 1)
	go func() {
		log.With("module", "acp2api").Info("listening",
			"addr", cfg.Addr,
			"workspace", workspaceDir,
			"agents", strings.Join(agentIDs(registry), ","),
			"auth", cfg.Token != "",
			// Redacted: a proxy URL may carry credentials.
			"proxy", cfg.Proxy.Redacted(),
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.With("module", "acp2api").Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// showVersion is the native handler for `acp2api version`.
func showVersion(_ context.Context, nctx engine.NativeContext) error {
	_, err := fmt.Fprintln(nctx.Stdout, version.Name, version.Version)
	return err
}

// builtinModules lists the per-agent modules this build assembles.
//
// This is the edge the modules are wired at: the core never imports them, so
// adding an agent's knowledge means adding a line here and nothing else.
func builtinModules() []agent.Module {
	return []agent.Module{
		devin.New(),
	}
}

// agentIDs returns the configured agent ids, for the startup log line.
func agentIDs(registry *agent.Registry) []string {
	agents := registry.List()
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	return ids
}

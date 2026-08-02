// Command docsearch-mcp serves the document tools over MCP Streamable HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bamsammich/docsearch/internal/config"
	"github.com/bamsammich/docsearch/internal/httpx"
	"github.com/bamsammich/docsearch/internal/mcpserver"
	"github.com/bamsammich/docsearch/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfg     config.Config
		origins string
	)
	flag.StringVar(&cfg.Addr, "addr", "", "listen address (default 127.0.0.1:8765)")
	flag.StringVar(&cfg.DBPath, "db", "", "path to the SQLite database")
	flag.StringVar(&cfg.LibraryRoot, "root", "", "library root; the only paths add_document accepts")
	flag.StringVar(&origins, "allowed-origins", "", "comma-separated Origin allowlist")
	flag.BoolVar(&cfg.AllowPublicBind, "allow-public-bind", false,
		"permit binding a non-loopback address")
	flag.Parse()

	if origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
			}
		}
	}
	cfg.FromEnv()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := cfg.Validate(); err != nil {
		return err
	}
	if !config.IsLoopbackAddr(cfg.Addr) {
		log.Warn("binding a non-loopback address",
			"addr", cfg.Addr,
			"warning", "this service exposes a filesystem path parameter; it must sit behind "+
				"a trusted network boundary such as Tailscale, never a public listener")
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = st.Close() }()

	srv := mcpserver.New(mcpserver.Deps{
		Store:       st,
		LibraryRoot: cfg.LibraryRoot,
		Log:         log,
	})

	// The SDK owns session handling; we do not invent our own scheme.
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Logger: log},
	)

	mux := http.NewServeMux()
	// Auth and origin checks wrap only /mcp. Probes must stay unauthenticated.
	mux.Handle("/mcp", httpx.RequireAllowedOrigin(cfg.AllowedOrigins,
		httpx.RequireBearer(cfg.BearerToken, mcpHandler)))

	// Liveness: the process is up. Deliberately says nothing else.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	// Readiness: the database opens and the schema is present. Reports no
	// titles, paths or counts -- it is reachable without a token.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := st.Ready(ctx); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ready")
	})

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpx.LogRequests(log, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Info("docsearch-mcp listening",
		"addr", cfg.Addr, "db", cfg.DBPath, "root", cfg.LibraryRoot,
		"allowed_origins", cfg.AllowedOrigins)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("shut down cleanly")
	return nil
}

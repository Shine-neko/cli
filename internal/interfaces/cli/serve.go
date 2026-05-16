package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/awf-project/cli/internal/application"
	"github.com/awf-project/cli/internal/infrastructure/logger"
	"github.com/awf-project/cli/internal/infrastructure/store"
	"github.com/awf-project/cli/internal/infrastructure/xdg"
	"github.com/awf-project/cli/internal/interfaces/cli/ui"
	"github.com/awf-project/cli/internal/interfaces/httpserver"
	"github.com/spf13/cobra"
)

// newServeCommand returns the cobra.Command for `awf serve`.
//
// Read-only by design: it exposes workflow definitions, execution history,
// and a live tail of the audit log over HTTP so the web dashboard can
// render real runs. It never executes or mutates a workflow.
func newServeCommand(cfg *Config) *cobra.Command {
	var (
		addr   string
		webDir string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the read-only web dashboard and API",
		Long: `Start a local HTTP server exposing AWF's read-only state for the web dashboard.

Endpoints (all read-only — no workflow is executed or mutated):
  GET /api/workflows/{name}  Projected workflow graph
  GET /api/history           Recent executions
  GET /api/events            Live SSE tail of the audit log
  GET /                      Static dashboard (if --web-dir resolves)

For safety the server binds to 127.0.0.1 by default. Exposing it on a
network interface requires an explicit --addr (e.g. --addr 0.0.0.0:8787).

Examples:
  awf serve
  awf serve --addr 127.0.0.1:8787 --web-dir ../dashboard/dist`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, cfg, addr, webDir)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8787", "Listen address (host:port)")
	cmd.Flags().StringVar(&webDir, "web-dir", "", "Path to the built dashboard (dist/); auto-detected if empty")

	return cmd
}

func runServe(cmd *cobra.Command, cfg *Config, addr, webDir string) error {
	writer := ui.NewOutputWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg.OutputFormat, cfg.NoColor, cfg.NoHints)

	if addr == "" {
		return writeErrorAndExit(writer, errors.New("listen address required (--addr host:port)"), ExitUser)
	}

	// Read-only workflow loader: composite YAML repository, same resolution
	// order as `awf list` / `awf run`.
	loader := NewWorkflowRepository()

	// History store: same SQLite DB the CLI writes to.
	historyPath := filepath.Join(cfg.StoragePath, "history.db")
	historyStore, err := store.NewSQLiteHistoryStore(historyPath)
	if err != nil {
		return writeErrorAndExit(writer, fmt.Errorf("open history store: %w", err), ExitSystem)
	}
	defer func() { _ = historyStore.Close() }()

	log := logger.NewConsoleLogger(io.Discard, logger.LevelWarn, cfg.NoColor)
	historySvc := application.NewHistoryService(historyStore, log)

	// Live run state: same JSON store the executor checkpoints into
	// (cfg.StoragePath/states) and the TUI polls for live monitoring.
	stateStore := store.NewJSONStore(filepath.Join(cfg.StoragePath, "states"))

	resolvedWebDir := resolveWebDir(webDir)
	srv := httpserver.NewServer(loader, historySvc, stateStore, auditLogPath(), resolvedWebDir)

	// Addr is intentionally unset: httpSrv.Serve(listener) ignores it and the
	// bound listener is the single source of truth for the address.
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return writeErrorAndExit(writer, fmt.Errorf("bind %s: %w", addr, err), ExitSystem)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "AWF dashboard API listening on http://%s\n", addr)
	if resolvedWebDir == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "  (no dashboard build found — serving API only; run `bun run build` in dashboard/)")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "  dashboard: %s\n", resolvedWebDir)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "  read-only — Ctrl+C to stop")

	errCh := make(chan error, 1)
	go func() {
		if serveErr := httpSrv.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := httpSrv.Shutdown(shutdownCtx); shutdownErr != nil {
			_ = httpSrv.Close()
		}
		fmt.Fprintln(cmd.OutOrStdout(), "\nstopped")
		return nil
	case serveErr := <-errCh:
		return writeErrorAndExit(writer, fmt.Errorf("server: %w", serveErr), ExitSystem)
	}
}

// auditLogPath mirrors the TUI's resolution: AWF_AUDIT_LOG env override,
// else the default XDG location. The file need not exist yet — the tailer
// waits for it.
func auditLogPath() string {
	if env := os.Getenv("AWF_AUDIT_LOG"); env != "" && env != "off" {
		return filepath.Clean(env)
	}
	return filepath.Join(xdg.AWFDataDir(), "audit.jsonl")
}

// absDir returns the absolute form of dir, falling back to the cleaned
// relative path if the working directory cannot be resolved.
func absDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return filepath.Clean(dir)
}

// resolveWebDir honors an explicit --web-dir, else auto-detects a built
// dashboard relative to the working directory.
func resolveWebDir(explicit string) string {
	if explicit != "" {
		clean := filepath.Clean(explicit)
		if info, statErr := os.Stat(clean); statErr == nil && info.IsDir() {
			return absDir(clean)
		}
		return ""
	}
	for _, c := range []string{"dashboard/dist", "../dashboard/dist"} {
		if info, statErr := os.Stat(c); statErr == nil && info.IsDir() {
			return absDir(c)
		}
	}
	return ""
}

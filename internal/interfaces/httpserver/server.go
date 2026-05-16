package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"github.com/awf-project/cli/internal/domain/workflow"
)

// workflowLoader is the read-only port the server needs to project a graph.
// Satisfied by *repository.YAMLRepository (and any composite repository).
type workflowLoader interface {
	Load(ctx context.Context, name string) (*workflow.Workflow, error)
}

// historyLister is the read-only port for past executions.
// Satisfied by *application.HistoryService.
type historyLister interface {
	List(ctx context.Context, filter *workflow.HistoryFilter) ([]*workflow.ExecutionRecord, error)
}

// stateReader is the read-only port for live run snapshots. Satisfied by
// *store.JSONStore — the same store the executor checkpoints into and the
// TUI polls for live monitoring.
type stateReader interface {
	List(ctx context.Context) ([]string, error)
	Load(ctx context.Context, workflowID string) (*workflow.ExecutionContext, error)
}

// Server holds the read-only dependencies and the resolved audit log path.
type Server struct {
	loader    workflowLoader
	history   historyLister
	state     stateReader
	auditPath string
	webDir    string
}

// NewServer wires the read-only dependencies. webDir may be "" (API only).
func NewServer(loader workflowLoader, history historyLister, state stateReader, auditPath, webDir string) *Server {
	return &Server{loader: loader, history: history, state: state, auditPath: auditPath, webDir: webDir}
}

// Handler builds the HTTP mux. All routes are read-only by construction:
// none of them execute or mutate workflow state.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/workflows/", s.handleWorkflow)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/runs/", s.handleRunSnapshot)
	mux.HandleFunc("/api/runs", s.handleRunList)
	mux.HandleFunc("/api/events", s.handleEvents)

	if s.webDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.webDir)))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{
				"service": "awf serve",
				"note":    "API only — no web dir configured. Endpoints: /api/workflows/{name}, /api/history, /api/events",
			})
		})
	}
	return mux
}

// GET /api/workflows/{name} — projected graph of a workflow definition.
func (s *Server) handleWorkflow(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Path)
	if name == "" || name == "workflows" {
		writeError(w, http.StatusBadRequest, "workflow name required: /api/workflows/{name}")
		return
	}

	wf, err := s.loader.Load(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("load workflow %q: %v", name, err))
		return
	}
	writeJSON(w, http.StatusOK, graphFromWorkflow(wf))
}

// GET /api/history — recent executions, newest first.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	records, err := s.history.List(r.Context(), &workflow.HistoryFilter{Limit: 50})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list history: %v", err))
		return
	}
	out := make([]HistoryDTO, 0, len(records))
	for _, rec := range records {
		out = append(out, historyFromRecord(rec))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/runs — active/recent runs (one per persisted state file),
// newest update first. The dashboard uses this to pick the latest run.
func (s *Server) handleRunList(w http.ResponseWriter, r *http.Request) {
	ids, err := s.state.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list runs: %v", err))
		return
	}

	out := make([]RunRefDTO, 0, len(ids))
	for _, id := range ids {
		ec, loadErr := s.state.Load(r.Context(), id)
		if loadErr != nil || ec == nil {
			continue // a run that vanished between List and Load is skipped
		}
		out = append(out, runRefFromContext(ec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	writeJSON(w, http.StatusOK, out)
}

// GET /api/runs/{id} — live snapshot of one run: per-node status used to
// color the graph. 404 if the run no longer exists.
func (s *Server) handleRunSnapshot(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(r.URL.Path)
	if id == "" || id == "runs" {
		writeError(w, http.StatusBadRequest, "run id required: /api/runs/{id}")
		return
	}

	ec, err := s.state.Load(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("load run %q: %v", id, err))
		return
	}
	if ec == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("run %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, snapshotFromContext(ec))
}

// GET /api/events — Server-Sent Events stream tailing the audit log.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ctx := r.Context()
	events := tailAudit(ctx, s.auditPath)

	// Heartbeat keeps proxies from closing an idle connection.
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case ev, open := <-events:
			if !open {
				return
			}
			payload, err := json.Marshal(logFromAuditEvent(&ev))
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The header (and likely body) is already on the wire; the client
		// will see a truncated response. Nothing actionable server-side.
		return
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

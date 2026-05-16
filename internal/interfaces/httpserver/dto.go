// Package httpserver exposes AWF's read-only execution state over HTTP so the
// web dashboard (see ../../../dashboard) can render real workflows and tail
// live runs. It is a thin interface adapter: it consumes the existing
// application/infrastructure services and never executes workflows itself.
//
// Security posture: read-only. No endpoint mutates state or runs a workflow.
// The server binds to 127.0.0.1 by default; exposing it requires an explicit
// --addr flag.
package httpserver

// The DTOs below are the wire contract with the dashboard. Their JSON shape
// mirrors dashboard/src/awf/types.ts so the frontend needs no remapping.

// NodeDTO is one workflow state, projected from workflow.Step.
type NodeDTO struct {
	ID             string    `json:"id"`
	Label          string    `json:"label"`
	Type           string    `json:"type"` // step | agent | parallel | for_each | terminal
	Status         string    `json:"status"`
	DurationSec    *int      `json:"durationSec,omitempty"`
	Agent          *AgentDTO `json:"agent,omitempty"`
	Children       []string  `json:"children,omitempty"`
	TerminalStatus string    `json:"terminalStatus,omitempty"`
}

// AgentDTO carries provider/model metadata for agent steps.
type AgentDTO struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	TokensIn  int    `json:"tokensIn"`
	TokensOut int    `json:"tokensOut"`
}

// EdgeDTO is a state transition.
type EdgeDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // on_success | on_failure | branch
}

// GraphDTO is the full workflow definition the dashboard renders.
type GraphDTO struct {
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Initial string    `json:"initial"`
	Nodes   []NodeDTO `json:"nodes"`
	Edges   []EdgeDTO `json:"edges"`
}

// HistoryDTO is one past execution, projected from workflow.ExecutionRecord.
type HistoryDTO struct {
	ID           string `json:"id"`
	WorkflowName string `json:"workflowName"`
	Status       string `json:"status"`
	StartedAt    string `json:"startedAt"`
	DurationMs   int64  `json:"durationMs"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// LogDTO is one tailed audit-log line, projected from workflow.AuditEvent.
// It maps onto the dashboard's LogEntry shape.
type LogDTO struct {
	TS      string `json:"ts"`     // HH:MM:SS
	Level   string `json:"level"`  // info | agent | parallel | error | success
	Source  string `json:"source"` // workflow / step name
	Message string `json:"message"`
}

// RunRefDTO identifies a persisted run (one JSON state file).
type RunRefDTO struct {
	ID           string `json:"id"`
	WorkflowName string `json:"workflowName"`
	Status       string `json:"status"`
	CurrentStep  string `json:"currentStep"`
	UpdatedAt    string `json:"updatedAt"`
}

// RunSnapshotDTO is the live state of one run, projected from
// workflow.ExecutionContext. The dashboard polls this to color the graph:
// NodeStatus maps a state name to its current run status so the frontend
// can overlay pending/running/success/failure onto the static graph.
type RunSnapshotDTO struct {
	ID           string                `json:"id"`
	WorkflowName string                `json:"workflowName"`
	Status       string                `json:"status"`
	CurrentStep  string                `json:"currentStep"`
	UpdatedAt    string                `json:"updatedAt"`
	NodeStatus   map[string]NodeRunDTO `json:"nodeStatus"`
}

// NodeRunDTO is the live status of a single state within a run.
type NodeRunDTO struct {
	Status      string `json:"status"` // pending | running | success | failure
	DurationSec *int   `json:"durationSec,omitempty"`
	TokensIn    int    `json:"tokensIn,omitempty"`
	TokensOut   int    `json:"tokensOut,omitempty"`
}

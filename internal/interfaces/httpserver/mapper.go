package httpserver

import (
	"time"

	"github.com/awf-project/cli/internal/domain/workflow"
)

// graphFromWorkflow projects a parsed workflow definition into the dashboard
// graph DTO. Static structure only: every node starts "pending"; live status
// is overlaid client-side from the SSE audit stream.
func graphFromWorkflow(wf *workflow.Workflow) GraphDTO {
	g := GraphDTO{
		Name:    wf.Name,
		Version: wf.Version,
		Initial: wf.Initial,
	}

	for name, step := range wf.Steps {
		node := NodeDTO{
			ID:     name,
			Label:  name,
			Type:   nodeType(step),
			Status: "pending",
		}

		if step.Agent != nil {
			node.Agent = &AgentDTO{
				Provider: step.Agent.Provider,
				Model:    optionString(step.Agent.Options, "model"),
			}
		}
		if step.Type == workflow.StepTypeParallel {
			node.Children = append(node.Children, step.Branches...)
		}
		if step.Type == workflow.StepTypeTerminal {
			node.TerminalStatus = string(step.Status)
		}

		g.Nodes = append(g.Nodes, node)
		g.Edges = append(g.Edges, edgesFromStep(name, step)...)
	}

	return g
}

// nodeType maps AWF's internal step typing onto the dashboard vocabulary.
// AWF has no dedicated "agent" StepType: an agent step is a command-typed
// step carrying an Agent config, so we surface that distinction explicitly.
func nodeType(step *workflow.Step) string {
	if step.Agent != nil {
		return "agent"
	}
	switch step.Type {
	case workflow.StepTypeParallel:
		return "parallel"
	case workflow.StepTypeTerminal:
		return "terminal"
	case workflow.StepTypeForEach, workflow.StepTypeWhile:
		return "for_each"
	default:
		return "step"
	}
}

func edgesFromStep(name string, step *workflow.Step) []EdgeDTO {
	var edges []EdgeDTO

	for _, branch := range step.Branches {
		edges = append(edges, EdgeDTO{From: name, To: branch, Kind: "branch"})
	}
	if step.OnSuccess != "" {
		edges = append(edges, EdgeDTO{From: name, To: step.OnSuccess, Kind: "on_success"})
	}
	if step.OnFailure != "" {
		edges = append(edges, EdgeDTO{From: name, To: step.OnFailure, Kind: "on_failure"})
	}
	return edges
}

func optionString(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	if v, ok := opts[key].(string); ok {
		return v
	}
	return ""
}

// historyFromRecord projects a persisted execution record.
func historyFromRecord(r *workflow.ExecutionRecord) HistoryDTO {
	return HistoryDTO{
		ID:           r.ID,
		WorkflowName: r.WorkflowName,
		Status:       r.Status,
		StartedAt:    r.StartedAt.Format(time.RFC3339),
		DurationMs:   r.DurationMs,
		ErrorMessage: r.ErrorMessage,
	}
}

// normalizeStatus maps AWF's ExecutionStatus vocabulary onto the dashboard's
// RunStatus vocabulary.
func normalizeStatus(s workflow.ExecutionStatus) string {
	switch s {
	case workflow.StatusRunning:
		return "running"
	case workflow.StatusCompleted:
		return "success"
	case workflow.StatusFailed, workflow.StatusCancelled:
		return "failure"
	default:
		return "pending"
	}
}

// runRefFromContext projects a persisted run into a lightweight reference.
func runRefFromContext(ec *workflow.ExecutionContext) RunRefDTO {
	return RunRefDTO{
		ID:           ec.WorkflowID,
		WorkflowName: ec.WorkflowName,
		Status:       normalizeStatus(ec.Status),
		CurrentStep:  ec.CurrentStep,
		UpdatedAt:    ec.UpdatedAt.Format(time.RFC3339),
	}
}

// snapshotFromContext projects an ExecutionContext into the live snapshot the
// dashboard polls to color the graph. The currently-executing step is forced
// to "running" even though its StepState may not exist yet (it is created when
// the step finishes), so the graph reflects in-flight work.
func snapshotFromContext(ec *workflow.ExecutionContext) RunSnapshotDTO {
	nodeStatus := make(map[string]NodeRunDTO)

	//nolint:gocritic // rangeValCopy: GetAllStepStates returns a defensive value-semantic copy by design; ranging by value is consistent with the domain (see ExecutionContext.GetAllStepStates)
	for name, st := range ec.GetAllStepStates() {
		node := NodeRunDTO{
			Status:    normalizeStatus(st.Status),
			TokensIn:  st.TokensInput,
			TokensOut: st.TokensOutput,
		}
		if !st.StartedAt.IsZero() && !st.CompletedAt.IsZero() {
			d := int(st.CompletedAt.Sub(st.StartedAt).Seconds())
			node.DurationSec = &d
		}
		nodeStatus[name] = node
	}

	// Overlay the in-flight step.
	if ec.Status == workflow.StatusRunning && ec.CurrentStep != "" {
		cur := nodeStatus[ec.CurrentStep]
		if cur.Status == "" || cur.Status == "pending" {
			cur.Status = "running"
			nodeStatus[ec.CurrentStep] = cur
		}
	}

	return RunSnapshotDTO{
		ID:           ec.WorkflowID,
		WorkflowName: ec.WorkflowName,
		Status:       normalizeStatus(ec.Status),
		CurrentStep:  ec.CurrentStep,
		UpdatedAt:    ec.UpdatedAt.Format(time.RFC3339),
		NodeStatus:   nodeStatus,
	}
}

// logFromAuditEvent projects one audit-log line into a dashboard log row.
// Event names follow AWF's taxonomy (workflow.started, step.completed, …);
// we derive a display level/source without interpreting the payload deeply.
func logFromAuditEvent(e *workflow.AuditEvent) LogDTO {
	level := "info"
	switch {
	case e.Error != "" || e.Status == "failed" || e.Status == "failure":
		level = "error"
	case e.Status == "success":
		level = "success"
	}

	source := e.WorkflowName
	if source == "" {
		source = e.ExecutionID
	}

	msg := e.Event
	if e.Status != "" {
		msg += " (" + e.Status + ")"
	}
	if e.Error != "" {
		msg += ": " + e.Error
	}

	return LogDTO{
		TS:      e.Timestamp.Format("15:04:05"),
		Level:   level,
		Source:  source,
		Message: msg,
	}
}

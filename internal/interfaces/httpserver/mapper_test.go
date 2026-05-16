package httpserver

import (
	"sort"
	"testing"
	"time"

	"github.com/awf-project/cli/internal/domain/workflow"
)

func TestNodeType(t *testing.T) {
	tests := []struct {
		name string
		step *workflow.Step
		want string
	}{
		{
			name: "agent config wins over command type",
			step: &workflow.Step{Type: workflow.StepTypeCommand, Agent: &workflow.AgentConfig{Provider: "claude"}},
			want: "agent",
		},
		{
			name: "explicit agent step type without agent config still maps to agent",
			step: &workflow.Step{Type: workflow.StepTypeAgent, Agent: &workflow.AgentConfig{Provider: "claude"}},
			want: "agent",
		},
		{
			name: "parallel",
			step: &workflow.Step{Type: workflow.StepTypeParallel},
			want: "parallel",
		},
		{
			name: "terminal",
			step: &workflow.Step{Type: workflow.StepTypeTerminal},
			want: "terminal",
		},
		{
			name: "for_each",
			step: &workflow.Step{Type: workflow.StepTypeForEach},
			want: "for_each",
		},
		{
			name: "while is surfaced as for_each (dashboard loop vocabulary)",
			step: &workflow.Step{Type: workflow.StepTypeWhile},
			want: "for_each",
		},
		{
			name: "plain command",
			step: &workflow.Step{Type: workflow.StepTypeCommand},
			want: "step",
		},
		{
			name: "unknown plugin step type falls back to step",
			step: &workflow.Step{Type: workflow.StepTypeOperation},
			want: "step",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeType(tt.step); got != tt.want {
				t.Errorf("nodeType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEdgesFromStep(t *testing.T) {
	step := &workflow.Step{
		Branches:  []string{"b1", "b2"},
		OnSuccess: "next",
		OnFailure: "handler",
	}

	edges := edgesFromStep("src", step)

	want := []EdgeDTO{
		{From: "src", To: "b1", Kind: "branch"},
		{From: "src", To: "b2", Kind: "branch"},
		{From: "src", To: "next", Kind: "on_success"},
		{From: "src", To: "handler", Kind: "on_failure"},
	}
	if len(edges) != len(want) {
		t.Fatalf("got %d edges, want %d: %+v", len(edges), len(want), edges)
	}
	for i := range want {
		if edges[i] != want[i] {
			t.Errorf("edge[%d] = %+v, want %+v", i, edges[i], want[i])
		}
	}
}

func TestEdgesFromStep_NoTransitions(t *testing.T) {
	if got := edgesFromStep("lonely", &workflow.Step{}); len(got) != 0 {
		t.Errorf("expected no edges for a step with no transitions, got %+v", got)
	}
}

func TestGraphFromWorkflow(t *testing.T) {
	wf := &workflow.Workflow{
		Name:    "demo",
		Version: "1.2.3",
		Initial: "start",
		Steps: map[string]*workflow.Step{
			"start": {
				Type:      workflow.StepTypeCommand,
				Agent:     &workflow.AgentConfig{Provider: "claude", Options: map[string]any{"model": "opus"}},
				OnSuccess: "fan",
			},
			"fan": {
				Type:     workflow.StepTypeParallel,
				Branches: []string{"a", "b"},
			},
			"done": {
				Type:   workflow.StepTypeTerminal,
				Status: workflow.TerminalSuccess,
			},
		},
	}

	g := graphFromWorkflow(wf)

	if g.Name != "demo" || g.Version != "1.2.3" || g.Initial != "start" {
		t.Fatalf("graph header mismatch: %+v", g)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(g.Nodes))
	}

	byID := make(map[string]NodeDTO, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
		if n.Status != "pending" {
			t.Errorf("node %q: static graph must start pending, got %q", n.ID, n.Status)
		}
	}

	start := byID["start"]
	if start.Type != "agent" || start.Agent == nil {
		t.Errorf("start node: want agent type with Agent set, got %+v", start)
	}
	if start.Agent.Provider != "claude" || start.Agent.Model != "opus" {
		t.Errorf("start agent metadata mismatch: %+v", start.Agent)
	}

	fan := byID["fan"]
	if fan.Type != "parallel" {
		t.Errorf("fan node type = %q, want parallel", fan.Type)
	}
	sort.Strings(fan.Children)
	if len(fan.Children) != 2 || fan.Children[0] != "a" || fan.Children[1] != "b" {
		t.Errorf("fan children = %+v, want [a b]", fan.Children)
	}

	done := byID["done"]
	if done.TerminalStatus != string(workflow.TerminalSuccess) {
		t.Errorf("done terminalStatus = %q, want %q", done.TerminalStatus, workflow.TerminalSuccess)
	}
}

func TestOptionString(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		key  string
		want string
	}{
		{"nil map", nil, "model", ""},
		{"missing key", map[string]any{"x": "y"}, "model", ""},
		{"wrong type", map[string]any{"model": 42}, "model", ""},
		{"string value", map[string]any{"model": "sonnet"}, "model", "sonnet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := optionString(tt.opts, tt.key); got != tt.want {
				t.Errorf("optionString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeStatus(t *testing.T) {
	tests := []struct {
		in   workflow.ExecutionStatus
		want string
	}{
		{workflow.StatusRunning, "running"},
		{workflow.StatusCompleted, "success"},
		{workflow.StatusFailed, "failure"},
		{workflow.StatusCancelled, "failure"},
		{workflow.ExecutionStatus("anything-else"), "pending"},
	}
	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			if got := normalizeStatus(tt.in); got != tt.want {
				t.Errorf("normalizeStatus(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHistoryFromRecord(t *testing.T) {
	started := time.Date(2026, 5, 16, 10, 30, 0, 0, time.UTC)
	rec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "deploy",
		Status:       "failed",
		StartedAt:    started,
		DurationMs:   1234,
		ErrorMessage: "boom",
	}

	got := historyFromRecord(rec)

	if got.ID != "exec-1" || got.WorkflowName != "deploy" || got.Status != "failed" {
		t.Errorf("identity fields mismatch: %+v", got)
	}
	if got.StartedAt != started.Format(time.RFC3339) {
		t.Errorf("StartedAt = %q, want RFC3339 %q", got.StartedAt, started.Format(time.RFC3339))
	}
	if got.DurationMs != 1234 || got.ErrorMessage != "boom" {
		t.Errorf("duration/error mismatch: %+v", got)
	}
}

func TestSnapshotFromContext_OverlaysRunningStep(t *testing.T) {
	start := time.Date(2026, 5, 16, 9, 0, 0, 0, time.UTC)
	ec := workflow.NewExecutionContext("run-1", "deploy")
	ec.Status = workflow.StatusRunning
	ec.CurrentStep = "deploying"
	ec.UpdatedAt = start.Add(30 * time.Second)
	ec.States = map[string]workflow.StepState{
		"build": {
			Status:       workflow.StatusCompleted,
			StartedAt:    start,
			CompletedAt:  start.Add(10 * time.Second),
			TokensInput:  100,
			TokensOutput: 200,
		},
		// "deploying" has no StepState yet — it is the in-flight step.
	}

	snap := snapshotFromContext(ec)

	if snap.ID != "run-1" || snap.WorkflowName != "deploy" {
		t.Fatalf("identity mismatch: %+v", snap)
	}
	if snap.Status != "running" || snap.CurrentStep != "deploying" {
		t.Errorf("status/currentStep mismatch: %+v", snap)
	}

	build := snap.NodeStatus["build"]
	if build.Status != "success" {
		t.Errorf("build status = %q, want success", build.Status)
	}
	if build.DurationSec == nil || *build.DurationSec != 10 {
		t.Errorf("build duration = %v, want 10s", build.DurationSec)
	}
	if build.TokensIn != 100 || build.TokensOut != 200 {
		t.Errorf("build tokens mismatch: %+v", build)
	}

	deploying := snap.NodeStatus["deploying"]
	if deploying.Status != "running" {
		t.Errorf("in-flight step must be forced to running, got %q", deploying.Status)
	}
}

func TestSnapshotFromContext_DoesNotOverrideKnownStatus(t *testing.T) {
	ec := workflow.NewExecutionContext("run-2", "wf")
	ec.Status = workflow.StatusRunning
	ec.CurrentStep = "step1"
	ec.States = map[string]workflow.StepState{
		"step1": {Status: workflow.StatusCompleted},
	}

	snap := snapshotFromContext(ec)

	// CurrentStep already has a terminal StepState: the overlay must not
	// downgrade a real success back to "running".
	if got := snap.NodeStatus["step1"].Status; got != "success" {
		t.Errorf("step1 status = %q, want success (overlay must not clobber)", got)
	}
}

func TestRunRefFromContext(t *testing.T) {
	updated := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	ec := workflow.NewExecutionContext("run-3", "nightly")
	ec.Status = workflow.StatusCompleted
	ec.CurrentStep = "report"
	ec.UpdatedAt = updated

	ref := runRefFromContext(ec)

	if ref.ID != "run-3" || ref.WorkflowName != "nightly" {
		t.Errorf("identity mismatch: %+v", ref)
	}
	if ref.Status != "success" || ref.CurrentStep != "report" {
		t.Errorf("status/step mismatch: %+v", ref)
	}
	if ref.UpdatedAt != updated.Format(time.RFC3339) {
		t.Errorf("UpdatedAt = %q, want RFC3339 %q", ref.UpdatedAt, updated.Format(time.RFC3339))
	}
}

func TestLogFromAuditEvent(t *testing.T) {
	ts := time.Date(2026, 5, 16, 14, 25, 36, 0, time.UTC)

	tests := []struct {
		name       string
		event      workflow.AuditEvent
		wantLevel  string
		wantSource string
		wantMsg    string
	}{
		{
			name:       "error from Error field",
			event:      workflow.AuditEvent{Event: "step.failed", Timestamp: ts, WorkflowName: "deploy", Error: "exit 1"},
			wantLevel:  "error",
			wantSource: "deploy",
			wantMsg:    "step.failed: exit 1",
		},
		{
			name:       "error from failed status",
			event:      workflow.AuditEvent{Event: "step.completed", Timestamp: ts, WorkflowName: "deploy", Status: "failed"},
			wantLevel:  "error",
			wantSource: "deploy",
			wantMsg:    "step.completed (failed)",
		},
		{
			name:       "success status",
			event:      workflow.AuditEvent{Event: "workflow.completed", Timestamp: ts, WorkflowName: "deploy", Status: "success"},
			wantLevel:  "success",
			wantSource: "deploy",
			wantMsg:    "workflow.completed (success)",
		},
		{
			name:       "info default, source falls back to execution id",
			event:      workflow.AuditEvent{Event: "workflow.started", Timestamp: ts, ExecutionID: "exec-9"},
			wantLevel:  "info",
			wantSource: "exec-9",
			wantMsg:    "workflow.started",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := logFromAuditEvent(&tt.event)
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q", got.Level, tt.wantLevel)
			}
			if got.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tt.wantSource)
			}
			if got.Message != tt.wantMsg {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMsg)
			}
			if got.TS != ts.Format("15:04:05") {
				t.Errorf("TS = %q, want %q", got.TS, ts.Format("15:04:05"))
			}
		})
	}
}

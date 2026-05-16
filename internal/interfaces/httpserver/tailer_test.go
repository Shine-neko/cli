package httpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/awf-project/cli/internal/domain/workflow"
)

// recvEvent waits for one event with a generous timeout so the polling
// tailer (1s file-wait, 500ms read-retry) has time to react.
func recvEvent(t *testing.T, ch <-chan workflow.AuditEvent) workflow.AuditEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before an event was received")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an audit event")
		return workflow.AuditEvent{}
	}
}

func writeLine(t *testing.T, f *os.File, ev *workflow.AuditEvent) {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatalf("write line: %v", err)
	}
}

func TestTailAudit_WaitsForFileThenStreamsNewLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start tailing before the file exists: the tailer must poll for it.
	ch := tailAudit(ctx, path)

	time.Sleep(200 * time.Millisecond) // ensure tailer is in its file-wait loop

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("create audit file: %v", err)
	}
	defer func() { _ = f.Close() }()

	// The tailer seeks to EOF on open, so a line written before it has
	// opened+seeked would be skipped by design. Give it time to get past
	// the 1s file-wait poll and seek, then append.
	time.Sleep(1500 * time.Millisecond)
	writeLine(t, f, &workflow.AuditEvent{Event: "workflow.started", WorkflowName: "demo"})

	got := recvEvent(t, ch)
	if got.Event != "workflow.started" || got.WorkflowName != "demo" {
		t.Errorf("unexpected event: %+v", got)
	}
}

func TestTailAudit_DoesNotReplayExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("create audit file: %v", err)
	}
	defer func() { _ = f.Close() }()

	// Pre-existing line that must NOT be replayed (tailer starts at EOF).
	writeLine(t, f, &workflow.AuditEvent{Event: "old.event"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := tailAudit(ctx, path)

	time.Sleep(300 * time.Millisecond) // let the tailer seek to end

	writeLine(t, f, &workflow.AuditEvent{Event: "new.event"})

	got := recvEvent(t, ch)
	if got.Event != "new.event" {
		t.Errorf("expected only new.event, got %q (old content replayed?)", got.Event)
	}
}

// TestTailAudit_PartialLine reproduces the bug fixed in #6: a line read by
// the tailer before its trailing newline has been written must not be parsed
// as truncated JSON nor cause a desync — it must be held until the newline
// arrives and then parsed whole.
func TestTailAudit_PartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("create audit file: %v", err)
	}
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := tailAudit(ctx, path)

	time.Sleep(300 * time.Millisecond)

	b, err := json.Marshal(workflow.AuditEvent{Event: "split.event", WorkflowName: "wf"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	half := len(b) / 2

	// First half — no newline. The tailer reads it (ReadBytes returns it on
	// EOF) and must buffer it instead of trying to unmarshal a fragment.
	if _, err := f.Write(b[:half]); err != nil {
		t.Fatalf("write first half: %v", err)
	}
	time.Sleep(700 * time.Millisecond) // longer than the 500ms read-retry

	// Second half + newline: now the full line is available.
	if _, err := f.Write(append(b[half:], '\n')); err != nil {
		t.Fatalf("write second half: %v", err)
	}

	got := recvEvent(t, ch)
	if got.Event != "split.event" || got.WorkflowName != "wf" {
		t.Errorf("split line not reassembled correctly: %+v", got)
	}
}

func TestTailAudit_CancelClosesChannel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	ch := tailAudit(ctx, path)

	cancel() // cancel while still waiting for the file to appear

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected no event, channel should just close on cancel")
		}
	case <-time.After(3 * time.Second):
		t.Error("channel was not closed after context cancellation")
	}
}

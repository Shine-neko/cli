package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/awf-project/cli/internal/domain/workflow"
)

// tailAudit follows the JSONL audit log like `tail -f`, emitting each parsed
// AuditEvent on the returned channel until ctx is cancelled. It starts at EOF
// (only new events are streamed); the dashboard loads the last run from
// /api/history separately, so we deliberately do not replay the whole file.
//
// Robust to the file not existing yet (audit logging may be disabled or no
// run has happened): it polls until the file appears.
func tailAudit(ctx context.Context, path string) <-chan workflow.AuditEvent {
	out := make(chan workflow.AuditEvent)

	go func() {
		defer close(out)

		var file *os.File
		// Wait for the file to exist.
		for file == nil {
			f, err := os.Open(path) //nolint:gosec // path derived from XDG/env, not user request body
			if err == nil {
				file = f
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		defer func() { _ = file.Close() }()

		// Seek to end: stream only new lines.
		if _, err := file.Seek(0, 2); err != nil {
			return
		}

		reader := bufio.NewReader(file)
		// pending accumulates a line read across multiple ReadBytes calls.
		// ReadBytes returns the bytes read so far even on EOF, so a line
		// written without its trailing '\n' yet (reader caught the writer
		// mid-append) must be held until the newline arrives — otherwise we
		// would unmarshal truncated JSON and resume from the line's middle.
		var pending []byte
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			chunk, err := reader.ReadBytes('\n')
			pending = append(pending, chunk...)

			if err == nil {
				// Complete line (newline reached).
				var ev workflow.AuditEvent
				if json.Unmarshal(pending, &ev) == nil {
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
				pending = pending[:0]
				continue
			}

			// EOF or partial line: keep `pending` and wait for more bytes.
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}()

	return out
}

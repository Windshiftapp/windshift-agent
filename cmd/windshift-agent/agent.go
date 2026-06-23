package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	chmctx "windshift-agent/internal/ctx"
	"windshift-agent/internal/llm"
	"windshift-agent/internal/tools"
)

// maxTurns bounds the agentic loop (one turn = one model response, plus any
// tool calls it makes). A model stuck calling tools forever would otherwise run
// until the runner's idle watchdog kills it; this fails fast with a clear event.
const maxTurns = 200

// readBufMax caps a single JSONL line on stdin (matches JSONL runner's 1 MiB).
const readBufMax = 1 << 20

// maxImagesPerRun bounds how many images view_image may load into context over a
// whole run — a backstop on token/request-body cost (each image is large) until
// broker-side token metering enforces a quota.
const maxImagesPerRun = 10

// streamRetries bounds re-issues of one model call after a transient stream
// error; streamRetryBase scales the linear backoff between attempts.
const (
	streamRetries   = 3
	streamRetryBase = 2 * time.Second
)

// agent holds the run-independent wiring; serve() drives the JSONL protocol.
type agent struct {
	client  *llm.Client
	tools   []llm.Tool
	ctxSize int

	mu  sync.Mutex // serializes stdout writes across emit calls
	out *bufio.Writer
}

func newAgent(client *llm.Client, toolset []llm.Tool, ctxSize int, stdout io.Writer) *agent {
	return &agent{
		client:  client,
		tools:   toolset,
		ctxSize: ctxSize,
		out:     bufio.NewWriter(stdout),
	}
}

// command is one inbound JSONL line on stdin.
type command struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// emit writes one NDJSON event line to stdout and flushes it. Every line is
// valid JSON so JSONL runner forwards it verbatim rather than wrapping it.
func (a *agent) emit(ev map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = a.out.Write(b)
	_ = a.out.WriteByte('\n')
	_ = a.out.Flush()
}

// postFinishComment posts the agent's closing summary to the assigned work item
// as a comment via the preconfigured ws CLI — the item's human-facing record of
// what the run did. It runs on every finish so the item is never left silent
// after a code-delivering run (the harness opens the PR but writes nothing on
// the item). Best-effort: a missing item id or empty summary skips silently,
// and a failure is surfaced as an event but never blocks the finish. ws is run
// with argv (never `sh -c`) so the model-authored summary can't be reinterpreted
// as shell syntax.
func (a *agent) postFinishComment(ctx context.Context, summary string) {
	itemID := strings.TrimSpace(os.Getenv("WINDSHIFT_ITEM_ID"))
	summary = strings.TrimSpace(summary)
	if itemID == "" || summary == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ws", "comment", "add", itemID, "-m", summary)
	if out, err := cmd.CombinedOutput(); err != nil {
		a.emit(map[string]any{
			"type":  "comment_failed",
			"error": strings.TrimSpace(string(out)),
		})
	}
}

// selfCommentedOnItem reports whether a bash command posted a comment on this
// run's work item through the ws CLI — `ws comment add <item> -m …`, the
// human-facing channel the prompt points the agent at. It matches the
// ws/comment/add verb sequence plus a reference to the item: either the literal
// id or the WINDSHIFT_ITEM_ID variable the prompt tells the agent to use. Used
// to suppress the duplicate finish comment when the agent already spoke for
// itself this run (WI-471). Heuristic by design — it shadows the documented
// command, not every conceivable way to reach the API.
func selfCommentedOnItem(command, itemID string) bool {
	if itemID == "" {
		return false
	}
	if !strings.Contains(command, itemID) && !strings.Contains(command, "WINDSHIFT_ITEM_ID") {
		return false
	}
	fields := strings.Fields(command)
	for i := 0; i+2 < len(fields); i++ {
		if fields[i] == "ws" && fields[i+1] == "comment" && fields[i+2] == "add" {
			return true
		}
	}
	return false
}

// serve runs the JSONL control loop until stdin closes, returning the process
// exit code. A prompt starts a run in its own goroutine; abort (or stdin EOF)
// cancels the in-flight run. JSONL runner sends one prompt, waits for session_idle,
// then sends abort + closes stdin — but serve tolerates repeated prompts too.
func (a *agent) serve(parent context.Context, stdin io.Reader) int {
	cmds := make(chan command, 8)
	go readCommands(stdin, cmds)

	var (
		runCancel context.CancelFunc
		runDone   chan struct{}
	)
	// stopRun cancels the in-flight run (if any) and waits for it to drain, so
	// we never leave a run goroutine writing to stdout after we move on.
	stopRun := func() {
		if runCancel != nil {
			runCancel()
		}
		if runDone != nil {
			<-runDone
		}
		runCancel, runDone = nil, nil
	}

	for c := range cmds {
		switch c.Type {
		case "prompt":
			stopRun() // one run at a time
			runCtx, cancel := context.WithCancel(parent)
			done := make(chan struct{})
			runCancel, runDone = cancel, done
			go func(prompt string) {
				defer close(done)
				a.runPrompt(runCtx, prompt)
				// Always announce idle on natural completion (success OR error)
				// so JSONL runner stops waiting; skip it when we were cancelled,
				// because the runner has already moved to shutdown.
				if runCtx.Err() == nil {
					a.emit(map[string]any{"type": "session_idle"})
				}
			}(c.Message)
		case "abort":
			stopRun()
		default:
			// ignore unknown commands
		}
	}

	// stdin closed: cancel any in-flight run and exit cleanly.
	stopRun()
	return 0
}

// readCommands parses one JSON object per line from stdin onto cmds, closing it
// at EOF. Blank and malformed lines are skipped.
func readCommands(stdin io.Reader, cmds chan<- command) {
	defer close(cmds)
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64*1024), readBufMax)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c command
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue
		}
		cmds <- c
	}
}

// runPrompt executes the agentic loop for a single user prompt: call the model,
// run any tool calls it returns, feed results back, repeat until it answers
// without tools (or errors / is cancelled). It does not emit session_idle —
// serve owns that so the cancel case can suppress it.
func (a *agent) runPrompt(ctx context.Context, prompt string) {
	a.emit(map[string]any{"type": "starting"})

	messages := []chmctx.Message{
		{Role: chmctx.RoleSystem, Content: systemPrompt},
		{Role: chmctx.RoleUser, Content: prompt},
	}
	ctxSize := a.ctxSize

	// itemID identifies the run's work item; selfCommented records whether the
	// agent already posted a comment on it this run (via `ws comment add`). The
	// finish guard then skips its auto-comment so the item isn't double-posted
	// (WI-471). Tracked across turns, so a comment in an early turn still
	// suppresses the guard at finish.
	itemID := strings.TrimSpace(os.Getenv("WINDSHIFT_ITEM_ID"))
	selfCommented := false
	// imagesInjected bounds how many images view_image may pull into context over
	// the whole run, a backstop on token/request-body cost until broker-side token
	// metering enforces a quota.
	imagesInjected := 0

	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return
		}

		packed := chmctx.Pack(messages, chmctx.Budget(ctxSize))

		// One model call, with bounded retry: a transient stream break (an
		// HTTP/2 reset between agent and broker, a proxy hiccup) used to end
		// the whole run mid-task. The request is idempotent — the packed
		// messages are still in hand — so re-issue it a few times before
		// declaring the run dead. Only the final, unrecovered error becomes
		// an error event (which the runner maps to a failed run).
		var (
			final     *chmctx.Message
			ctxWindow int
			err       error
		)
		for attempt := 1; ; attempt++ {
			final, ctxWindow, err = a.collect(ctx, a.client.Chat(ctx, packed.Messages, a.tools))
			if ctx.Err() != nil {
				return
			}
			if err == nil || attempt >= streamRetries {
				break
			}
			a.emit(map[string]any{"type": "retry", "message": fmt.Sprintf("stream error (attempt %d/%d), retrying: %v", attempt, streamRetries, err)})
			select {
			case <-time.After(time.Duration(attempt) * streamRetryBase):
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			a.emit(map[string]any{"type": "error", "message": err.Error()})
			return
		}
		if final == nil {
			a.emit(map[string]any{"type": "error", "message": "no response from model"})
			return
		}
		if ctxWindow > 0 {
			ctxSize = ctxWindow // server-authoritative window for the next pack
		}

		messages = append(messages, *final)

		if len(final.ToolCalls) == 0 {
			if final.Content != "" {
				a.emit(map[string]any{"type": "message", "role": "assistant", "text": final.Content})
			}
			return // task complete
		}

		// pendingImages holds images that view_image fetched this turn. They are
		// injected as fresh user messages only AFTER every tool result for the
		// turn is written (below the loop): Chat Completions requires every tool
		// call in an assistant turn to be answered by a tool result before any
		// other message, so a synthetic user image message inserted between two
		// tool results would be invalid ordering.
		var pendingImages []chmctx.ImageContent
		for _, tc := range final.ToolCalls {
			if ctx.Err() != nil {
				return
			}
			// finish ends the run with a structured outcome instead of tool
			// output: emit the event and stop. Any tool calls the model
			// queued after finish are dropped — it declared itself done.
			if tc.Name == tools.FinishName {
				outcome, _ := tc.Arguments["outcome"].(string)
				summary, _ := tc.Arguments["summary"].(string)
				// Record the run's closing summary on the work item itself, the
				// human-facing channel: the harness opens the PR but narrates
				// nothing on the item, so without this a code-delivering run
				// leaves the item silent. Best-effort, before the finish event.
				// Skipped when the agent already commented on the item itself
				// this run (WI-471) — the guard only exists to break silence, so
				// a second auto-comment on top of the agent's own would just
				// duplicate it.
				if !selfCommented {
					a.postFinishComment(ctx, summary)
				}
				a.emit(map[string]any{"type": "finish", "outcome": outcome, "summary": summary})
				return
			}
			a.emit(map[string]any{"type": "tool_start", "id": tc.ID, "tool": tc.Name, "args": tc.Arguments})
			result := tools.Execute(ctx, tc)
			a.emit(map[string]any{"type": "tool_done", "id": tc.ID, "tool": tc.Name, "output": result.Content})
			// An image-bearing result (view_image) carries its image on the tool
			// message only as a courier; image parts are invalid on the tool role,
			// so move them to pendingImages and clear the courier before the tool
			// message enters history.
			if len(result.Images) > 0 {
				pendingImages = append(pendingImages, result.Images...)
				result.Images = nil
			}
			messages = append(messages, result)
			// Note an agent self-comment on the item so the finish guard above
			// can stand down (WI-471). Only count a clean exit — a failed
			// `ws comment add` posted nothing, so the guard must still fire.
			if !selfCommented && tc.Name == tools.BashName {
				if cmd, _ := tc.Arguments["cmd"].(string); selfCommentedOnItem(cmd, itemID) && !strings.Contains(result.Content, "(exit:") {
					selfCommented = true
				}
			}
		}

		// Deferred image injection: now that every tool result for this turn is
		// written, append one user message per fetched image so the next model
		// turn can see it. Past the per-run cap, tell the model the image was
		// skipped rather than silently dropping it.
		for _, img := range pendingImages {
			if imagesInjected >= maxImagesPerRun {
				messages = append(messages, chmctx.Message{
					Role:    chmctx.RoleUser,
					Content: fmt.Sprintf("Image from %s was not loaded: this run's image limit (%d) is reached.", img.Source, maxImagesPerRun),
				})
				continue
			}
			messages = append(messages, chmctx.Message{
				Role:    chmctx.RoleUser,
				Content: fmt.Sprintf("Image loaded from %s:", img.Source),
				Images:  []chmctx.ImageContent{img},
			})
			imagesInjected++
		}
	}

	a.emit(map[string]any{"type": "error", "message": "reached maximum turns without completing"})
}

// collect drains one Chat stream: it forwards incremental content as events and
// returns the final assistant message, the server-reported context window (0 if
// none), and the first stream error. Incremental tool-call/reasoning events are
// ignored — the resolved tool calls arrive whole on the final message.
func (a *agent) collect(ctx context.Context, events <-chan llm.Event) (*chmctx.Message, int, error) {
	var (
		final     *chmctx.Message
		ctxWindow int
		streamErr error
	)
	for ev := range events {
		switch ev.Kind {
		case llm.EventContent:
			if ev.Content != "" {
				a.emit(map[string]any{"type": "content", "text": ev.Content})
			}
		case llm.EventDone:
			final = ev.Final
			ctxWindow = ev.ContextWindow
		case llm.EventError:
			streamErr = ev.Err
		}
	}
	return final, ctxWindow, streamErr
}

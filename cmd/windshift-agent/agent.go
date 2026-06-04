package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"

	chmctx "windshift-agent/internal/ctx"
	"windshift-agent/internal/llm"
	"windshift-agent/internal/tools"
)

// maxTurns bounds the agentic loop (one turn = one model response, plus any
// tool calls it makes). A model stuck calling tools forever would otherwise run
// until the runner's idle watchdog kills it; this fails fast with a clear event.
const maxTurns = 200

// readBufMax caps a single JSONL line on stdin (matches PiRunner's 1 MiB).
const readBufMax = 1 << 20

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
// valid JSON so PiRunner forwards it verbatim rather than wrapping it.
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

// serve runs the JSONL control loop until stdin closes, returning the process
// exit code. A prompt starts a run in its own goroutine; abort (or stdin EOF)
// cancels the in-flight run. PiRunner sends one prompt, waits for session_idle,
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
				// so PiRunner stops waiting; skip it when we were cancelled,
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

	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return
		}

		packed := chmctx.Pack(messages, chmctx.Budget(ctxSize))
		final, ctxWindow, err := a.collect(ctx, a.client.Chat(ctx, packed.Messages, a.tools))
		if ctx.Err() != nil {
			return
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

		for _, tc := range final.ToolCalls {
			if ctx.Err() != nil {
				return
			}
			a.emit(map[string]any{"type": "tool_start", "id": tc.ID, "tool": tc.Name, "args": tc.Arguments})
			result := tools.Execute(ctx, tc)
			a.emit(map[string]any{"type": "tool_done", "id": tc.ID, "tool": tc.Name, "output": result.Content})
			messages = append(messages, result)
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

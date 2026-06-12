// Command windshift-agent is the thin native coding agent that runs inside the
// Windshift runner's throwaway container (Initiative WI-204). It is a forked,
// stripped descendant of codehamr (MIT): the TUI, self-updater, and config-file
// bootstrap are gone; what remains is the OpenAI-compatible LLM client, the
// context packer, and the four coding tools (bash/read/write/edit).
//
// The agent is the UNTRUSTED payload. It holds no SCM credentials and chooses
// nothing about what to clone or push: the runner prepares /workspace and
// brokers every outbound secret. The agent reaches the model only through the
// Windshift llm-proxy (OpenAI-compatible /v1/chat/completions), configured
// entirely from the environment.
//
// It speaks the JSONL subprocess contract that services.JSONL runner drives:
//
//	stdin  (one JSON object per line):
//	    {"type":"prompt","message":"..."}   run the task
//	    {"type":"abort"}                     cancel the in-flight run
//	    (stdin EOF)                          shut down cleanly
//	stdout (NDJSON events, one per line):
//	    {"type":"starting"}
//	    {"type":"content","text":"..."}
//	    {"type":"tool_start","id":..,"tool":..,"args":{..}}
//	    {"type":"tool_done","id":..,"tool":..,"output":".."}
//	    {"type":"message","role":"assistant","text":".."}
//	    {"type":"error","message":".."}
//	    {"type":"session_idle"}              run finished (success or error)
//
// JSONL runner blocks until it sees session_idle, then sends abort + closes stdin.
//
// TODO(WI-208): flip the llm client to stream:false for the non-streaming MVP.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"

	"windshift-agent/internal/llm"
	"windshift-agent/internal/tools"
)

//go:embed system.md
var systemPrompt string

type config struct {
	baseURL string
	model   string
	token   string
	ctxSize int
}

const defaultContextSize = 128_000

func configFromEnv() (config, error) {
	c := config{
		baseURL: os.Getenv("LLM_BASE_URL"),
		model:   os.Getenv("LLM_MODEL"),
		token:   os.Getenv("LLM_API_KEY"),
		ctxSize: defaultContextSize,
	}
	if c.baseURL == "" {
		return c, fmt.Errorf("LLM_BASE_URL is required")
	}
	if c.model == "" {
		return c, fmt.Errorf("LLM_MODEL is required")
	}
	if v := os.Getenv("LLM_CONTEXT_SIZE"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			c.ctxSize = n
		}
	}
	return c, nil
}

// toolDefs is the OpenAI tool catalog the agent advertises to the model. Each
// *Schema() returns the complete tool object (name/description/parameters); we
// lift them into llm.Tool so the advertised names always match tools.Execute's
// dispatch (bash/read_file/write_file/edit_file).
func toolDefs() []llm.Tool {
	schemas := []map[string]any{
		tools.BashSchema(),
		tools.ReadFileSchema(),
		tools.WriteFileSchema(),
		tools.EditFileSchema(),
	}
	out := make([]llm.Tool, 0, len(schemas))
	for _, s := range schemas {
		fn, _ := s["function"].(map[string]any)
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		out = append(out, llm.Tool{
			Type:     "function",
			Function: llm.FunctionDef{Name: name, Description: desc, Parameters: params},
		})
	}
	return out
}

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "windshift-agent:", err)
		os.Exit(2)
	}

	a := newAgent(llm.New(cfg.baseURL, cfg.model, cfg.token), toolDefs(), cfg.ctxSize, os.Stdout)
	os.Exit(a.serve(context.Background(), os.Stdin))
}

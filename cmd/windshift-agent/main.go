// Command windshift-agent is the thin native coding agent that runs inside the
// Windshift runner's throwaway container (Initiative WI-204). It is a forked,
// stripped descendant of codehamr (MIT): the TUI, self-updater, and config-file
// bootstrap are gone; what remains is the OpenAI-compatible LLM client, the
// context packer, and the four coding tools (bash/read/write/edit).
//
// The agent is the UNTRUSTED payload. It holds no SCM credentials and chooses
// nothing about what to clone or where to push: the runner prepares /workspace
// and brokers every outbound secret. The agent reaches the model only through
// the Windshift llm-proxy (OpenAI-compatible /v1/chat/completions), configured
// entirely from the environment:
//
//	LLM_BASE_URL  llm-proxy base, e.g. http://llm-proxy/v1 (required)
//	LLM_MODEL     model id the proxy routes (required)
//	LLM_API_KEY   per-run broker token; never a raw provider key (optional)
//
// TODO(WI-207): drive the JSONL subprocess contract over stdin/stdout
//   - stdin:  {"type":"prompt","message":"..."}  {"type":"abort"}
//   - stdout: starting / tool_start / tool_done / error / final / session_idle
// TODO(WI-208): flip the llm client to stream:false for the non-streaming MVP.
package main

import (
	"fmt"
	"os"

	"windshift-agent/internal/llm"
	"windshift-agent/internal/tools"
)

type config struct {
	baseURL string
	model   string
	token   string
}

func configFromEnv() (config, error) {
	c := config{
		baseURL: os.Getenv("LLM_BASE_URL"),
		model:   os.Getenv("LLM_MODEL"),
		token:   os.Getenv("LLM_API_KEY"),
	}
	if c.baseURL == "" {
		return c, fmt.Errorf("LLM_BASE_URL is required")
	}
	if c.model == "" {
		return c, fmt.Errorf("LLM_MODEL is required")
	}
	return c, nil
}

// toolDefs is the OpenAI tool catalog the agent advertises to the model. Each
// schema comes from the forked tools package; dispatch is tools.Execute.
func toolDefs() []llm.Tool {
	defs := []struct {
		name, desc string
		params     map[string]any
	}{
		{"bash", "Run a shell command in the workspace.", tools.BashSchema()},
		{"read_file", "Read a file from the workspace.", tools.ReadFileSchema()},
		{"write_file", "Write (create or overwrite) a file in the workspace.", tools.WriteFileSchema()},
		{"edit_file", "Replace an exact substring in a file.", tools.EditFileSchema()},
	}
	out := make([]llm.Tool, 0, len(defs))
	for _, d := range defs {
		out = append(out, llm.Tool{
			Type:     "function",
			Function: llm.FunctionDef{Name: d.name, Description: d.desc, Parameters: d.params},
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

	client := llm.New(cfg.baseURL, cfg.model, cfg.token)
	catalog := toolDefs()

	// TODO(WI-207): replace this banner with the JSONL stdin/stdout loop that
	// drives client.Chat(...) and tools.Execute(...) per prompt.
	fmt.Fprintf(os.Stderr,
		"windshift-agent: ready (model=%s, base=%s, tools=%d); JSONL loop pending WI-207\n",
		cfg.model, cfg.baseURL, len(catalog))
	_ = client
}

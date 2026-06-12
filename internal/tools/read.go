package tools

import (
	"fmt"
	"os"

	chmctx "windshift-agent/internal/ctx"
)

// ReadFile returns path's contents, truncated to the shared tool-output budget
// (Truncate). The model gets exact bytes, not a shell-mangled approximation.
// Per the bash/write/edit convention, filesystem errors come back in the output
// string, never as a Go error: the model reacts to them like a non-zero exit.
func ReadFile(path string) string {
	if path == "" {
		return "(empty path)"
	}
	resolved, rerr := resolveWorkspacePath(path, false)
	if rerr != nil {
		return fmt.Sprintf("(path error: %v)", rerr)
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Sprintf("(read error: %v)", err)
	}
	return chmctx.Truncate(string(raw))
}

// ReadFileSchema is the OpenAI tool definition for read_file. The description
// nudges the model toward read_file over `cat` so it stops piping source
// through the shell just to look at it.
func ReadFileSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        ReadFileName,
			"description": "Read a file and return its contents. Prefer this over `cat`/`sed` in bash for inspecting a file - no shell quoting, exact bytes. Output over 6k tokens is truncated to first+last 2k; for a slice of a large file use bash with sed/grep/head/tail.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path under /workspace. Relative paths resolve against /workspace; absolute paths outside /workspace and '..' are rejected.",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

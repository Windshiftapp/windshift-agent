package tools

import (
	"fmt"
	"os"
	"strings"

	chmctx "windshift-agent/internal/ctx"
)

// ReadFile returns path's contents, truncated to the shared tool-output budget
// (Truncate). The model gets exact bytes, not a shell-mangled approximation.
// Per the bash/write/edit convention, filesystem errors come back in the output
// string, never as a Go error: the model reacts to them like a non-zero exit.
//
// offset/limit slice by line: offset is the 1-based first line, limit the
// maximum number of lines; zero means from-the-start / to-the-end. The slice
// is exact bytes too — no line numbers are injected, so a returned span
// anchors edit_file's old_string verbatim.
func ReadFile(path string, offset, limit int) string {
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
	content := string(raw)
	if offset <= 0 && limit <= 0 {
		return chmctx.Truncate(content)
	}
	lines := strings.Split(content, "\n")
	total := len(lines)
	start := max(offset, 1)
	if start > total {
		return fmt.Sprintf("(offset %d is past the end - file has %d lines)", start, total)
	}
	end := total
	if limit > 0 && start-1+limit < end {
		end = start - 1 + limit
	}
	header := fmt.Sprintf("(lines %d-%d of %d)\n", start, end, total)
	return header + chmctx.Truncate(strings.Join(lines[start-1:end], "\n"))
}

// ReadFileSchema is the OpenAI tool definition for read_file. The description
// nudges the model toward read_file over `cat` so it stops piping source
// through the shell just to look at it.
func ReadFileSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        ReadFileName,
			"description": "Read a file and return its contents. Prefer this over `cat`/`sed` in bash for inspecting a file - no shell quoting, exact bytes. Output over 6k tokens is truncated to first+last 2k; for a slice of a large file pass offset/limit instead of shelling out to sed.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path under /workspace. Relative paths resolve against /workspace; absolute paths outside /workspace and '..' are rejected.",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "1-based first line to return. Default: start of file.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of lines to return. Default: to end of file.",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

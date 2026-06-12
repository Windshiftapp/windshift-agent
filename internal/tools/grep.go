package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	chmctx "windshift-agent/internal/ctx"
)

// grepTimeout bounds one search; a pathological regex over a big tree should
// come back as a tool failure the model can react to, not hang the turn.
const grepTimeout = 30 * time.Second

// Grep searches file contents under /workspace and returns matching lines
// with file:line prefixes, optionally with surrounding context. Backed by
// ripgrep (shipped in the agent image) with a plain `grep -rn` fallback so
// the tool degrades instead of vanishing on a stripped environment. Like the
// other tools, every failure is reported in the output string, never as a Go
// error.
func Grep(parent context.Context, pattern, path, glob string, contextLines int) string {
	if parent.Err() != nil {
		return "(cancelled)"
	}
	if strings.TrimSpace(pattern) == "" {
		return "(empty pattern)"
	}
	if path == "" {
		path = "."
	}
	resolved, rerr := resolveWorkspacePath(path, false)
	if rerr != nil {
		return fmt.Sprintf("(path error: %v)", rerr)
	}
	// Search by relative path from the workspace root so match prefixes come
	// back repo-relative — the shape the model feeds into read_file/edit_file.
	rel, err := filepath.Rel(workspaceRoot, resolved)
	if err != nil {
		rel = resolved
	}

	ctxT, cancel := context.WithTimeout(parent, grepTimeout)
	defer cancel()

	bin, args := grepCommand(pattern, rel, glob, contextLines)
	cmd := exec.CommandContext(ctxT, bin, args...)
	cmd.Dir = workspaceRoot
	out, runErr := cmd.CombinedOutput()
	s := string(out)
	if runErr != nil {
		if ctxT.Err() == context.DeadlineExceeded {
			return s + fmt.Sprintf("\n(timeout after %s)", grepTimeout)
		}
		// Both rg and grep exit 1 for "no matches" with empty output.
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && strings.TrimSpace(s) == "" {
			return "(no matches)"
		}
		s += fmt.Sprintf("\n(exit: %v)", runErr)
	}
	if strings.TrimSpace(s) == "" {
		return "(no matches)"
	}
	return chmctx.Truncate(s)
}

// grepCommand builds the search invocation: ripgrep when present, POSIX
// grep -rn otherwise (which ignores glob filtering — noted in the output by
// the caller's schema docs rather than failing the call).
func grepCommand(pattern, rel, glob string, contextLines int) (string, []string) {
	if _, err := exec.LookPath("rg"); err == nil {
		args := []string{"--no-heading", "-n", "--max-columns", "400", "--max-columns-preview", "--no-follow"}
		if contextLines > 0 {
			args = append(args, "-C", strconv.Itoa(contextLines))
		}
		if glob != "" {
			args = append(args, "-g", glob)
		}
		args = append(args, "-e", pattern, "--", rel)
		return "rg", args
	}
	args := []string{"-rn", "-I"}
	if contextLines > 0 {
		args = append(args, "-C", strconv.Itoa(contextLines))
	}
	args = append(args, "-e", pattern, "--", rel)
	return "grep", args
}

// GrepSchema is the OpenAI tool definition for grep.
func GrepSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        GrepName,
			"description": "Search file contents under /workspace with a regex (ripgrep). Returns file:line matches, optionally with surrounding context lines — prefer one grep call with context over a grep+sed/read pair. Output is truncated if large; narrow with path/glob.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regex to search for (ripgrep syntax).",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "File or directory to search, relative to /workspace. Default: the whole workspace.",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": "Optional filename filter, e.g. \"*.go\" or \"**/*.svelte\".",
					},
					"context_lines": map[string]any{
						"type":        "integer",
						"description": "Lines of context to include before and after each match (like -C). Default 0.",
					},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

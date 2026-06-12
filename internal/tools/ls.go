package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	chmctx "windshift-agent/internal/ctx"
)

// listTimeout bounds one listing; huge trees should truncate, not hang.
const listTimeout = 15 * time.Second

// ListFiles enumerates files under a directory in /workspace, repo-relative,
// honoring .gitignore. Backed by `rg --files` (shipped in the agent image)
// rather than a hand-rolled tree walk — battle-tested ignore semantics for
// free — with a `find` fallback for stripped environments. Failures come
// back in the output string, matching the other tools' convention.
func ListFiles(parent context.Context, dir, glob string) string {
	if parent.Err() != nil {
		return "(cancelled)"
	}
	if dir == "" {
		dir = "."
	}
	resolved, rerr := resolveWorkspacePath(dir, false)
	if rerr != nil {
		return fmt.Sprintf("(path error: %v)", rerr)
	}
	rel, err := filepath.Rel(workspaceRoot, resolved)
	if err != nil {
		rel = resolved
	}

	ctxT, cancel := context.WithTimeout(parent, listTimeout)
	defer cancel()

	bin, args := listCommand(rel, glob)
	cmd := exec.CommandContext(ctxT, bin, args...)
	cmd.Dir = workspaceRoot
	out, runErr := cmd.CombinedOutput()
	s := string(out)
	if runErr != nil {
		if ctxT.Err() == context.DeadlineExceeded {
			return s + fmt.Sprintf("\n(timeout after %s)", listTimeout)
		}
		// rg exits 1 with no output when nothing matched the glob.
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && strings.TrimSpace(s) == "" {
			return "(no entries)"
		}
		s += fmt.Sprintf("\n(exit: %v)", runErr)
	}
	if strings.TrimSpace(s) == "" {
		return "(no entries)"
	}
	return chmctx.Truncate(s)
}

// listCommand builds the enumeration invocation: `rg --files` when present,
// POSIX find otherwise (which approximates the glob against base names and
// skips .git by hand).
func listCommand(rel, glob string) (string, []string) {
	if _, err := exec.LookPath("rg"); err == nil {
		args := []string{"--files", "--no-follow"}
		if glob != "" {
			args = append(args, "-g", glob)
		}
		args = append(args, "--", rel)
		return "rg", args
	}
	args := []string{rel, "-name", ".git", "-prune", "-o", "-type", "f"}
	if glob != "" {
		args = append(args, "-name", glob)
	}
	args = append(args, "-print")
	return "find", args
}

// ListFilesSchema is the OpenAI tool definition for list_files.
func ListFilesSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        ListFilesName,
			"description": "List files under a directory in /workspace recursively (repo-relative paths, .gitignore honored). Optional glob filter like \"*.go\" or \"**/handlers/*.go\". Prefer this over `ls -R`/`find` in bash.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dir": map[string]any{
						"type":        "string",
						"description": "Directory to list, relative to /workspace. Default: the workspace root.",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": "Optional glob filter on paths, e.g. \"*.svelte\" or \"internal/**/*.go\".",
					},
				},
				"required": []string{},
			},
		},
	}
}

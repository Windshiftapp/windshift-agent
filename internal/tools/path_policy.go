package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const workspaceRoot = "/workspace"

func resolveWorkspacePath(path string, forWrite bool) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if hasDotDot(path) {
		return "", fmt.Errorf("path escapes /workspace: '..' is not allowed")
	}
	root := filepath.Clean(workspaceRoot)
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		rootReal = root
	}

	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if !withinPath(root, candidate) {
		return "", fmt.Errorf("path escapes /workspace")
	}

	if real, err := filepath.EvalSymlinks(candidate); err == nil {
		if !withinPath(rootReal, real) {
			return "", fmt.Errorf("path escapes /workspace via symlink")
		}
		return real, nil
	} else if !forWrite {
		return candidate, nil // let the caller return the normal read/open error
	}

	parentReal, err := nearestExistingParentReal(candidate)
	if err != nil {
		return "", err
	}
	if !withinPath(rootReal, parentReal) {
		return "", fmt.Errorf("path escapes /workspace via symlink")
	}
	return candidate, nil
}

func nearestExistingParentReal(path string) (string, error) {
	p := filepath.Dir(path)
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return real, nil
		}
		next := filepath.Dir(p)
		if next == p {
			return "", fmt.Errorf("no existing parent for %s", path)
		}
		p = next
	}
}

func hasDotDot(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == os.PathSeparator }) {
		if part == ".." {
			return true
		}
	}
	return false
}

func withinPath(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(path, root+sep)
}

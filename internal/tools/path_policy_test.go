package tools

import "testing"

func TestResolveWorkspacePathRejectsEscapes(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "../outside", "/workspace/../etc/passwd", "/proc/self/environ"} {
		if _, err := resolveWorkspacePath(path, false); err == nil {
			t.Fatalf("resolveWorkspacePath(%q) succeeded, want rejection", path)
		}
	}
}

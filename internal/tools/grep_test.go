package tools

import (
	"strings"
	"testing"
)

func TestGrepCommandRipgrepArgs(t *testing.T) {
	bin, args := grepCommand("foo.*bar", "internal", "*.go", 3)
	joined := strings.Join(args, " ")
	if bin == "rg" {
		for _, want := range []string{"-C 3", "-g *.go", "-e foo.*bar", "-- internal"} {
			if !strings.Contains(joined, want) {
				t.Errorf("rg args missing %q: %s", want, joined)
			}
		}
	} else {
		// grep fallback: no glob support, but pattern and path must survive.
		for _, want := range []string{"-C 3", "-e foo.*bar", "-- internal"} {
			if !strings.Contains(joined, want) {
				t.Errorf("grep args missing %q: %s", want, joined)
			}
		}
	}
}

func TestGrepCommandNoOptionalArgs(t *testing.T) {
	_, args := grepCommand("x", ".", "", 0)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-C") || strings.Contains(joined, "-g ") {
		t.Errorf("unexpected optional flags without glob/context: %s", joined)
	}
}

func TestListCommandGlob(t *testing.T) {
	bin, args := listCommand(".", "*.svelte")
	joined := strings.Join(args, " ")
	switch bin {
	case "rg":
		if !strings.Contains(joined, "--files") || !strings.Contains(joined, "-g *.svelte") {
			t.Errorf("rg list args wrong: %s", joined)
		}
	case "find":
		if !strings.Contains(joined, "-name *.svelte") {
			t.Errorf("find list args wrong: %s", joined)
		}
	}
}

package tools

import (
	"context"
	"testing"

	chmctx "windshift-agent/internal/ctx"
)

func TestRunRawBashRejectsMissingOrBlankCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "missing", args: map[string]any{}},
		{name: "blank", args: map[string]any{"cmd": "  \t"}},
		{name: "wrong type", args: map[string]any{"cmd": 42}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := runRaw(context.Background(), chmctx.ToolCall{Name: BashName, Arguments: test.args})
			want := `(invalid arguments: bash requires a non-empty string "cmd"; retry the bash call with the intended command)`
			if got != want {
				t.Fatalf("runRaw() = %q, want %q", got, want)
			}
		})
	}
}

func TestBashSchemaRequiresNonEmptyCommand(t *testing.T) {
	t.Parallel()

	function := BashSchema()["function"].(map[string]any)
	parameters := function["parameters"].(map[string]any)
	properties := parameters["properties"].(map[string]any)
	command := properties["cmd"].(map[string]any)
	if got := command["minLength"]; got != 1 {
		t.Fatalf("cmd minLength = %v, want 1", got)
	}
}

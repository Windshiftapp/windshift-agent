package main

import (
	"testing"

	"windshift-agent/internal/tools"
)

// TestToolDefs_VisionGating verifies view_image is advertised only when vision
// is enabled, so a no-vision model never sees the tool (WI-488).
func TestToolDefs_VisionGating(t *testing.T) {
	has := func(supportsVision bool) bool {
		for _, d := range toolDefs(supportsVision) {
			if d.Function.Name == tools.ViewImageName {
				return true
			}
		}
		return false
	}
	if !has(true) {
		t.Error("view_image should be registered when vision is supported")
	}
	if has(false) {
		t.Error("view_image must NOT be registered when vision is unsupported")
	}
}

func TestConfigFromEnvRequiresResolvedModelLimits(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "http://broker")
	t.Setenv("LLM_MODEL", "model-a")
	t.Setenv("LLM_PROTOCOL", "chat_completions")
	if _, err := configFromEnv(); err == nil {
		t.Fatal("expected missing model limits to fail")
	}
	t.Setenv("LLM_CONTEXT_WINDOW", "200000")
	t.Setenv("LLM_MAX_TOKENS", "32000")
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ctxSize != 200000 || cfg.maxTokens != 32000 {
		t.Fatalf("limits = (%d, %d)", cfg.ctxSize, cfg.maxTokens)
	}
}

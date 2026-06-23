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

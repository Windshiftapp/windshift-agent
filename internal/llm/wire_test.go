package llm

import (
	"encoding/json"
	"strings"
	"testing"

	chmctx "windshift-agent/internal/ctx"
)

// TestToWire_ImageUserMessageEmitsParts verifies a user message carrying images
// serializes as a content-parts array: a leading text part then one image_url
// part per image.
func TestToWire_ImageUserMessageEmitsParts(t *testing.T) {
	wire := toWire([]chmctx.Message{{
		Role:    chmctx.RoleUser,
		Content: "look:",
		Images:  []chmctx.ImageContent{{DataURL: "data:image/png;base64,AAAA"}},
	}})
	parts, ok := wire[0].Content.([]contentPart)
	if !ok {
		t.Fatalf("expected []contentPart, got %T", wire[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("want 2 parts (text+image), got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "look:" {
		t.Errorf("first part should be the text, got %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,AAAA" {
		t.Errorf("second part should be the image_url, got %+v", parts[1])
	}
	// Marshals to the OpenAI shape.
	b, _ := json.Marshal(wire[0])
	if !strings.Contains(string(b), `"type":"image_url"`) || !strings.Contains(string(b), `"detail":"auto"`) {
		t.Errorf("marshaled message missing image_url part: %s", b)
	}
}

// TestToWire_ImageWithoutTextOmitsTextPart verifies an image-only user message
// produces just the image part (no empty leading text part).
func TestToWire_ImageWithoutTextOmitsTextPart(t *testing.T) {
	wire := toWire([]chmctx.Message{{
		Role:   chmctx.RoleUser,
		Images: []chmctx.ImageContent{{DataURL: "data:image/jpeg;base64,BBBB"}},
	}})
	parts := wire[0].Content.([]contentPart)
	if len(parts) != 1 || parts[0].Type != "image_url" {
		t.Fatalf("want a single image part, got %+v", parts)
	}
}

// TestToWire_PlainMessagesStayString verifies non-image messages keep the flat
// string form, including the deliberate empty-string tool result (Ollama 400s on
// a nil content). Images on a non-user role are ignored (spec disallows them).
func TestToWire_PlainMessagesStayString(t *testing.T) {
	wire := toWire([]chmctx.Message{
		{Role: chmctx.RoleTool, Content: "", ToolCallID: "c1"},
		{Role: chmctx.RoleAssistant, Content: "hi"},
		{Role: chmctx.RoleUser, Content: "no image"},
		{Role: chmctx.RoleTool, Content: "x", Images: []chmctx.ImageContent{{DataURL: "data:image/png;base64,Z"}}},
	})
	for i, w := range wire {
		if _, ok := w.Content.(string); !ok {
			t.Errorf("msg %d content should be string, got %T", i, w.Content)
		}
	}
	// Empty string must still serialize as "content":"" (not omitted / null).
	b, _ := json.Marshal(wire[0])
	if !strings.Contains(string(b), `"content":""`) {
		t.Errorf("empty tool content must serialize as \"\": %s", b)
	}
}

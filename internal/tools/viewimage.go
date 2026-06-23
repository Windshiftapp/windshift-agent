package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	chmctx "windshift-agent/internal/ctx"
)

// attachmentURLRe matches a canonical Windshift attachment download URL path.
// Only this exact shape is accepted from a URL argument — an arbitrary path that
// merely contains an "attachments" segment is rejected, so a forged or opaque
// URL can't be treated as a valid attachment reference.
var attachmentURLRe = regexp.MustCompile(`/rest/api/v1/attachments/(\d+)/download/?$`)

// ViewImageName is the wire name of the vision tool. It is registered only when
// the bound model supports vision (LLM_SUPPORTS_VISION), so a no-vision model
// never has image bytes injected into its context.
const ViewImageName = "view_image"

const (
	// maxImageBytes caps a single image's raw size before base64 (which inflates
	// it ~33%). The broker caps the whole request body at 16 MiB; keeping one
	// image well under that leaves room for history and other parts.
	maxImageBytes = 4 << 20 // 4 MiB
	// wsFetchTimeout bounds the run-scoped `ws` download.
	wsFetchTimeout = 30 * time.Second
)

// ViewImageSchema is the OpenAI tool definition for view_image.
func ViewImageSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        ViewImageName,
			"description": "Load an image attachment from the current work item into your visual context so you can actually see it. Pass the attachment id shown by `ws task get`. The image is fetched, then appears as an image in the next user message for you to analyse. Only image attachments (png, jpeg, webp, gif) are supported.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"attachment_id": map[string]any{
						"type":        "string",
						"description": "The numeric attachment id (preferred), or a Windshift attachment download URL.",
					},
				},
				"required": []string{"attachment_id"},
			},
		},
	}
}

// executeViewImage fetches an attachment via the run-scoped `ws` CLI, validates
// it is a supported image within the size cap, and returns a text tool result
// carrying the prepared image (ImageContent). The agent loop moves that image
// onto a fresh user message AFTER the whole tool-result batch is written, so a
// turn with several tool calls keeps valid tool-call ordering.
func executeViewImage(parent context.Context, call chmctx.ToolCall) chmctx.Message {
	id, err := parseAttachmentID(call.Arguments["attachment_id"])
	if err != nil {
		return toolText(call, fmt.Sprintf("(view_image: %v)", err))
	}
	data, err := fetchAttachment(parent, id)
	if err != nil {
		return toolText(call, fmt.Sprintf("(view_image: could not fetch attachment %d: %v)", id, err))
	}
	if len(data) == 0 {
		return toolText(call, fmt.Sprintf("(view_image: attachment %d is empty)", id))
	}
	if len(data) > maxImageBytes {
		return toolText(call, fmt.Sprintf("(view_image: attachment %d is %d bytes, over the %d-byte per-image limit; not loaded)", id, len(data), maxImageBytes))
	}
	mime, ok := sniffImageMime(data)
	if !ok {
		return toolText(call, fmt.Sprintf("(view_image: attachment %d is not a supported image type (png, jpeg, webp, gif))", id))
	}
	source := fmt.Sprintf("attachment %d", id)
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	return chmctx.Message{
		Role:       chmctx.RoleTool,
		Content:    fmt.Sprintf("Loaded image from %s (%s, %d bytes) into context.", source, mime, len(data)),
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Images:     []chmctx.ImageContent{{DataURL: dataURL, Source: source}},
	}
}

func toolText(call chmctx.ToolCall, text string) chmctx.Message {
	return chmctx.Message{
		Role:       chmctx.RoleTool,
		Content:    text,
		ToolCallID: call.ID,
		ToolName:   call.Name,
	}
}

// parseAttachmentID accepts a numeric id (the model may send it as a JSON number
// or string) or a Windshift attachment-download URL, from which the numeric id
// is parsed. A non-numeric, non-Windshift-attachment value is rejected — the
// fetch path is id-based, so an opaque URL it can't resolve must not slip in.
func parseAttachmentID(arg any) (int, error) {
	switch v := arg.(type) {
	case float64:
		if v <= 0 || v != math.Trunc(v) {
			return 0, fmt.Errorf("attachment_id must be a positive integer")
		}
		return int(v), nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, fmt.Errorf("attachment_id is required")
		}
		if n, err := strconv.Atoi(s); err == nil {
			if n <= 0 {
				return 0, fmt.Errorf("attachment_id must be a positive integer")
			}
			return n, nil
		}
		return idFromAttachmentURL(s)
	default:
		return 0, fmt.Errorf("attachment_id is required")
	}
}

// idFromAttachmentURL extracts the numeric id from a canonical Windshift
// attachment download URL (.../rest/api/v1/attachments/{id}/download). The path
// must match that exact shape, and an absolute URL's host must be this Windshift
// instance (from WS_API_URL) — so a non-Windshift or forged URL is rejected
// rather than treated as a valid attachment reference. A relative path (no host)
// is accepted as unambiguously a Windshift API path.
func idFromAttachmentURL(raw string) (int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, fmt.Errorf("not a numeric id or Windshift attachment URL")
	}
	if u.Host != "" {
		wh := windshiftHost()
		if wh == "" || !strings.EqualFold(u.Host, wh) {
			return 0, fmt.Errorf("attachment URL host is not this Windshift instance")
		}
	}
	m := attachmentURLRe.FindStringSubmatch(u.Path)
	if m == nil {
		return 0, fmt.Errorf("not a Windshift attachment download URL")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid attachment id in URL")
	}
	return n, nil
}

// windshiftHost is the host of this run's Windshift instance, taken from the
// WS_API_URL the runner injects. Empty when unset (then absolute URLs are
// rejected — the id-or-relative-path forms still work).
func windshiftHost() string {
	base := strings.TrimSpace(os.Getenv("WS_API_URL"))
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Host
}

// fetchAttachment streams an attachment's raw bytes through the run-scoped `ws`
// CLI, which already holds the per-run token (the upstream provider cannot fetch
// our auth-gated attachment URLs). argv form, never a shell, so a model-supplied
// id can't be reinterpreted as shell syntax.
func fetchAttachment(parent context.Context, id int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, wsFetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ws", "attachment", "download", strconv.Itoa(id), "--to", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", firstLine(msg))
	}
	return stdout.Bytes(), nil
}

// sniffImageMime identifies the supported image types by magic bytes. Explicit
// signatures (rather than http.DetectContentType) keep webp detection reliable
// and the allowed set tight.
func sniffImageMime(b []byte) (string, bool) {
	switch {
	case len(b) >= 8 && bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg", true
	case len(b) >= 6 && (bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))):
		return "image/gif", true
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp", true
	default:
		return "", false
	}
}

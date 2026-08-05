// Package llm adapts Windshift's run-scoped neutral inference operation to the
// coding agent's event contract. Provider protocols and credentials never
// enter the container.
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"windshift-agent/internal/cloud"
	chmctx "windshift-agent/internal/ctx"
)

type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Event struct {
	Kind          EventKind
	Content       string
	ContextWindow int
	ToolCall      *chmctx.ToolCall
	Final         *chmctx.Message
	Budget        cloud.BudgetStatus
	Tokens        int
	Elapsed       time.Duration
	Err           error
}

type EventKind int

const (
	EventContent EventKind = iota
	EventToolCall
	EventDone
	EventError
	EventReasoning
	EventToolArgs
)

type Client struct {
	Endpoint  string
	MaxTokens int
	Token     string
	http      *http.Client
}

// New constructs a client for the sole inference operation exposed by the
// broker. Model and protocol selection remain server-side in the run binding.
func New(endpoint, token string, maxTokens int) *Client {
	return &Client{
		Endpoint:  strings.TrimRight(endpoint, "/"),
		Token:     token,
		MaxTokens: maxTokens,
		http:      &http.Client{},
	}
}

func (c *Client) Chat(parent context.Context, messages []chmctx.Message, tools []Tool) <-chan Event {
	out := make(chan Event, 32)
	go c.runNeutral(parent, messages, tools, out)
	return out
}

func sendEvent(parent context.Context, out chan<- Event, event Event) bool {
	select {
	case out <- event:
		return true
	case <-parent.Done():
		return false
	}
}

type completionRequest struct {
	Messages  []wireMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
	Tools     []Tool        `json:"tools,omitempty"`
}

type wireMessage struct {
	Role            string           `json:"role"`
	Content         string           `json:"content"`
	Attachments     []wireAttachment `json:"attachments,omitempty"`
	ToolCalls       []wireToolCall   `json:"tool_calls,omitempty"`
	ToolCallID      string           `json:"tool_call_id,omitempty"`
	Name            string           `json:"name,omitempty"`
	ProviderState   json.RawMessage  `json:"provider_state,omitempty"`
	ProviderBinding string           `json:"provider_binding,omitempty"`
	CacheBreakpoint bool             `json:"cache_breakpoint,omitempty"`
}

type wireAttachment struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type completionResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Client) runNeutral(parent context.Context, messages []chmctx.Message, tools []Tool, out chan<- Event) {
	defer close(out)
	start := time.Now()
	mapped, err := toWireMessages(messages)
	if err != nil {
		sendEvent(parent, out, Event{Kind: EventError, Err: err})
		return
	}
	body, err := json.Marshal(completionRequest{Messages: mapped, MaxTokens: c.MaxTokens, Tools: tools})
	if err != nil {
		sendEvent(parent, out, Event{Kind: EventError, Err: err})
		return
	}
	request, err := http.NewRequestWithContext(parent, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		sendEvent(parent, out, Event{Kind: EventError, Err: err})
		return
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("Content-Type", "application/json")
	// Advertise the neutral inference contract version so the broker rejects a
	// stale client with a diagnostic 426 Upgrade Required instead of letting it
	// misparse the response (WI-921). Keep in lockstep with the broker's
	// brokerProtocolVersion.
	request.Header.Set("X-Protocol-Version", "1")
	response, err := c.http.Do(request)
	if err != nil {
		sendEvent(parent, out, Event{Kind: EventError, Err: err})
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		sendEvent(parent, out, Event{Kind: EventError, Err: mapBrokerError(response.StatusCode, payload)})
		return
	}
	var decoded completionResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		sendEvent(parent, out, Event{Kind: EventError, Err: fmt.Errorf("decode inference response: %w", err)})
		return
	}
	if len(decoded.Choices) == 0 {
		sendEvent(parent, out, Event{Kind: EventError, Err: fmt.Errorf("inference response contained no choices")})
		return
	}
	message, err := fromWireMessage(decoded.Choices[0].Message)
	if err != nil {
		sendEvent(parent, out, Event{Kind: EventError, Err: err})
		return
	}
	if message.Content != "" {
		sendEvent(parent, out, Event{Kind: EventContent, Content: message.Content, Budget: cloud.FromHeaders(response.Header)})
	}
	for i := range message.ToolCalls {
		call := message.ToolCalls[i]
		if !sendEvent(parent, out, Event{Kind: EventToolCall, ToolCall: &call, Budget: cloud.FromHeaders(response.Header)}) {
			return
		}
	}
	sendEvent(parent, out, Event{
		Kind: EventDone, Final: &message, Tokens: decoded.Usage.CompletionTokens,
		Budget: cloud.FromHeaders(response.Header), ContextWindow: cloud.ContextWindowFromHeaders(response.Header),
		Elapsed: time.Since(start),
	})
}

func toWireMessages(messages []chmctx.Message) ([]wireMessage, error) {
	out := make([]wireMessage, 0, len(messages))
	for _, message := range messages {
		mapped := wireMessage{
			Role: string(message.Role), Content: message.Content, ToolCallID: message.ToolCallID,
			Name: message.ToolName, ProviderState: message.ProviderState, ProviderBinding: message.ProviderBinding,
			CacheBreakpoint: message.CacheBreakpoint,
		}
		for _, image := range message.Images {
			mediaType, data, err := decodeDataURL(image.DataURL)
			if err != nil {
				return nil, err
			}
			mapped.Attachments = append(mapped.Attachments, wireAttachment{MimeType: mediaType, Data: base64.StdEncoding.EncodeToString(data)})
		}
		for _, call := range message.ToolCalls {
			arguments, err := json.Marshal(call.Arguments)
			if err != nil {
				return nil, err
			}
			wireCall := wireToolCall{ID: call.ID, Type: "function"}
			wireCall.Function.Name = call.Name
			wireCall.Function.Arguments = string(arguments)
			mapped.ToolCalls = append(mapped.ToolCalls, wireCall)
		}
		out = append(out, mapped)
	}
	return out, nil
}

func fromWireMessage(message wireMessage) (chmctx.Message, error) {
	mapped := chmctx.Message{
		Role: chmctx.Role(message.Role), Content: message.Content, ToolCallID: message.ToolCallID,
		ToolName: message.Name, ProviderState: message.ProviderState, ProviderBinding: message.ProviderBinding,
		CacheBreakpoint: message.CacheBreakpoint,
	}
	for _, call := range message.ToolCalls {
		arguments := map[string]any{}
		if call.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
				return chmctx.Message{}, fmt.Errorf("decode tool arguments for %s: %w", call.Function.Name, err)
			}
		}
		mapped.ToolCalls = append(mapped.ToolCalls, chmctx.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: arguments})
	}
	return mapped, nil
}

func mapBrokerError(status int, payload []byte) error {
	switch status {
	case http.StatusUnauthorized:
		return cloud.ErrUnauthorized
	case http.StatusPaymentRequired:
		return cloud.ErrBudgetExhausted
	default:
		return fmt.Errorf("inference broker returned HTTP %d: %s", status, strings.TrimSpace(string(payload)))
	}
}

func decodeDataURL(value string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") {
		return "", nil, fmt.Errorf("invalid image data URL")
	}
	mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	data, err := base64.StdEncoding.DecodeString(encoded)
	return mediaType, data, err
}

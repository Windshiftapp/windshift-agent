package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	chmctx "windshift-agent/internal/ctx"
)

func TestNeutralClientPostsSingleInferenceOperation(t *testing.T) {
	state := json.RawMessage(`{"provider":"opaque"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/llm-proxy/7/complete" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer run-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("X-Protocol-Version"); got != "1" {
			t.Fatalf("protocol version = %q, want %q", got, "1")
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := request["model"]; ok {
			t.Fatal("container must not select a provider model")
		}
		if _, ok := request["protocol"]; ok {
			t.Fatal("container must not select a provider protocol")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"","provider_state":{"provider":"opaque"},"provider_binding":"sha256:binding","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"README.md\"}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}
		}`))
	}))
	defer server.Close()

	client := New(server.URL+"/llm-proxy/7/complete", "run-token", 512)
	client.http = server.Client()
	events := client.Chat(context.Background(), []chmctx.Message{{
		Role: chmctx.RoleAssistant, ProviderState: state, ProviderBinding: "sha256:binding",
		ToolCalls: []chmctx.ToolCall{{ID: "old-call", Name: "old_tool", Arguments: map[string]any{"ok": true}}},
	}}, []Tool{{Type: "function", Function: FunctionDef{Name: "read_file", Parameters: map[string]any{"type": "object"}}}})

	var final *chmctx.Message
	for event := range events {
		if event.Err != nil {
			t.Fatalf("event error = %v", event.Err)
		}
		if event.Kind == EventDone {
			final = event.Final
			if event.Tokens != 4 {
				t.Fatalf("completion tokens = %d", event.Tokens)
			}
		}
	}
	if final == nil || len(final.ToolCalls) != 1 || final.ToolCalls[0].Name != "read_file" {
		t.Fatalf("final = %+v", final)
	}
	if string(final.ProviderState) != string(state) || final.ProviderBinding != "sha256:binding" {
		t.Fatalf("continuation = %s binding=%q", final.ProviderState, final.ProviderBinding)
	}
}

func TestNeutralClientMapsAuthorizationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	client := New(server.URL, "bad-token", 10)
	client.http = server.Client()
	for event := range client.Chat(context.Background(), []chmctx.Message{{Role: chmctx.RoleUser, Content: "hello"}}, nil) {
		if event.Kind == EventError && event.Err == nil {
			t.Fatal("authorization error event has nil error")
		}
	}
}

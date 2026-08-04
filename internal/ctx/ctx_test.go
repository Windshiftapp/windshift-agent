package ctx

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPackPinsStablePrefixAcrossAppendedTurns(t *testing.T) {
	history := []Message{
		{Role: RoleUser, Content: "Implement WI-904 in this repository."},
		{Role: RoleAssistant, Content: "I will inspect the inference path."},
		{Role: RoleUser, Content: "Continue."},
	}
	first := Pack(history, 10_000)
	second := Pack(append(history, Message{Role: RoleAssistant, Content: "The inspection is complete."}), 10_000)
	if first.StablePrefix != 1 || second.StablePrefix != 1 {
		t.Fatalf("stable prefix lengths = %d/%d, want 1/1", first.StablePrefix, second.StablePrefix)
	}
	if !reflect.DeepEqual(first.Messages[:first.StablePrefix], second.Messages[:second.StablePrefix]) {
		t.Fatalf("appending a turn changed the stable prefix:\nfirst=%+v\nsecond=%+v", first.Messages, second.Messages)
	}

	changed := slicesClone(history)
	changed[0].Content = "Implement a deliberately changed task."
	third := Pack(changed, 10_000)
	if reflect.DeepEqual(first.Messages[:first.StablePrefix], third.Messages[:third.StablePrefix]) {
		t.Fatal("a deliberate prefix change should invalidate the stable prefix")
	}
}

func TestPackKeepsProviderContinuationWithCompleteToolRoundAtBudgetBoundary(t *testing.T) {
	state := json.RawMessage(`{"anthropic":{"signature":"opaque"}}`)
	owner := Message{
		Role: RoleAssistant, ProviderState: state,
		ToolCalls: []ToolCall{{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "README.md"}}},
	}
	result := Message{Role: RoleTool, ToolCallID: "call-1", ToolName: "read_file", Content: "contents"}
	history := []Message{
		{Role: RoleUser, Content: "Inspect the repository."},
		{Role: RoleAssistant, Content: string(make([]byte, 8_000))},
		owner,
		result,
	}
	packed := Pack(history, owner.Tokens()+result.Tokens())
	if len(packed.Messages) != 3 {
		t.Fatalf("packed messages = %+v, want anchor plus complete tool round", packed.Messages)
	}
	if !reflect.DeepEqual(packed.Messages[1].ProviderState, state) || packed.Messages[1].ToolCalls[0].ID != "call-1" || packed.Messages[2].ToolCallID != "call-1" {
		t.Fatalf("continuation/tool pairing was broken: %+v", packed.Messages)
	}
}

func TestPackDropsPartiallyAnsweredParallelRoundWhole(t *testing.T) {
	history := []Message{
		{Role: RoleUser, Content: "Run both checks."},
		{
			Role: RoleAssistant, ProviderState: json.RawMessage(`{"reasoning":"opaque"}`),
			ToolCalls: []ToolCall{
				{ID: "call-1", Name: "check_one"},
				{ID: "call-2", Name: "check_two"},
			},
		},
		{Role: RoleTool, ToolCallID: "call-1", ToolName: "check_one", Content: "done"},
	}
	packed := Pack(history, 10_000)
	if len(packed.Messages) != 1 || packed.Messages[0].Role != RoleUser {
		t.Fatalf("partial parallel round reached the wire: %+v", packed.Messages)
	}
}

func slicesClone(in []Message) []Message {
	out := make([]Message, len(in))
	copy(out, in)
	return out
}

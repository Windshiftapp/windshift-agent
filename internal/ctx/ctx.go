// Package ctx owns conversation messages, tool-output truncation, and
// newest-first packing.
package ctx

import (
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ImageContent is an image attached to a user message, carried as a base64
// data URL. Only user messages may carry images: OpenAI chat-completions honors
// image content parts on the user role only (tool/system are text-only), so
// toWire emits image parts exclusively for the user role.
type ImageContent struct {
	DataURL string `json:"data_url"`
	Source  string `json:"source,omitempty"` // human label, e.g. "attachment 7 (mockup.png)"
}

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"name,omitempty"`
	// Images attaches base64 image parts to a user message (view_image flow).
	Images []ImageContent `json:"images,omitempty"`
	// ProviderState is opaque provider-owned continuation metadata. The packer
	// keeps it attached to the assistant turn that produced its tool calls.
	ProviderState   json.RawMessage `json:"provider_state,omitempty"`
	ProviderBinding string          `json:"provider_binding,omitempty"`
	CacheBreakpoint bool            `json:"cache_breakpoint,omitempty"`
}

// Tokens approximates token count as char/4, good enough for budgeting.
func Tokens(s string) int { return (len(s) + 3) / 4 }

func (m Message) Tokens() int {
	n := Tokens(m.Content)
	for _, tc := range m.ToolCalls {
		n += Tokens(tc.Name)
		for k, v := range tc.Arguments {
			n += Tokens(k) + Tokens(fmt.Sprint(v))
		}
	}
	// A base64 data URL is far cheaper in real image tokens than its char count
	// suggests, so budget a flat per-image estimate instead of len/4 (which would
	// wildly over-count and evict useful history).
	n += len(m.Images) * ImageTokenEstimate
	return n + 8
}

const (
	ToolOutputCap = 6000
	ToolHeadTail  = 2000
	// FixedSystem reserves budget for the embedded prompt + working-dir anchor
	// (see tui.buildSystem). PROMPT_SYS.md + anchor is ~3800 tokens (the
	// verification-honesty ledger, fail-fast install probe, and trace-read
	// fallback grew it); the buffer to 4000 keeps prompt edits from silently
	// over-budgeting small-ctx profiles. A tui test pins this against the live
	// prompt; bump here when it fails, never relax the assertion.
	FixedSystem = 4000
	FixedTools  = 1500
	// ImageTokenEstimate is the flat per-image budget for a base64 image part.
	// Real vision token cost is model-specific (hundreds–low thousands); this is
	// a rough proxy so an attached image consumes plausible budget without the
	// data URL's char count blowing up the packer.
	ImageTokenEstimate = 1200
)

// budgetHeadroomDivisor cuts the history budget by 1/this (10%) below the
// declared window. The char/4 Tokens heuristic UNDERcounts code- and JSON-heavy
// histories (the real tokenizer emits more per char), so packing to the literal
// ceiling risks the true token count spilling past the window: on Ollama a
// silent front-truncation that drops the system prompt and the anchored task, on
// llama.cpp a hard 400. The margin keeps an honest context_size safely in-window.
const budgetHeadroomDivisor = 10

// ResponseReserve is the slice Budget keeps free for the model's response.
// Scales as ctxSize/8 so reasoning models get room (262k→32k, 1M→125k),
// floored at 8k so small-ctx profiles don't collapse history to nothing.
func ResponseReserve(ctxSize int) int {
	if r := ctxSize / 8; r > 8000 {
		return r
	}
	return 8000
}

// Truncate collapses oversized tool outputs to first 2k + last 2k tokens;
// inputs at or under 6k pass through unchanged. Head/tail can't overlap:
// >6k tokens means >24k bytes, well over the 16k kept. Boundaries snap to a
// valid UTF-8 rune start so non-ASCII output never breaks mid-sequence.
func Truncate(out string) string {
	total := Tokens(out)
	if total <= ToolOutputCap {
		return out
	}
	limit := ToolHeadTail * 4
	head := runeBoundaryDown(out, limit)
	tail := runeBoundaryUp(out, len(out)-limit)
	marker := fmt.Sprintf("\n───── truncated: %d tokens total, first %d + last %d shown, the middle is OMITTED. This is a PARTIAL view; you can't review or conclude from code you can't see here, so re-run narrower (grep/sed/head/tail) to read the omitted span. ─────\n",
		total, ToolHeadTail, ToolHeadTail)
	return out[:head] + marker + out[tail:]
}

// runeBoundaryDown walks i left to a rune start so out[:i] never ends
// mid-sequence. Safe for i == len(out).
func runeBoundaryDown(out string, i int) int {
	if i >= len(out) {
		return len(out)
	}
	for i > 0 && !utf8.RuneStart(out[i]) {
		i--
	}
	return i
}

// runeBoundaryUp walks i right to a rune start so out[i:] never starts
// mid-sequence. Safe for i <= 0.
func runeBoundaryUp(out string, i int) int {
	if i <= 0 {
		return 0
	}
	for i < len(out) && !utf8.RuneStart(out[i]) {
		i++
	}
	return i
}

// PackResult records what Pack kept: the packed messages and their count.
type PackResult struct {
	Messages     []Message
	Kept         int
	StablePrefix int // number of leading messages pinned byte-for-byte across turns
}

// Pack pins the original user task as a stable prefix, then packs complete
// chronological units newest-first after that breakpoint. A tool-producing
// assistant and all of its tool results are one indivisible unit, which keeps
// opaque reasoning/thinking ProviderState paired with the calls it produced at
// every budget boundary. The newest valid unit is retained even over budget.
//
// System notes are normalized before selection, rather than rewriting the
// selected prefix afterward. Together with pinning the original task this means
// appending a turn cannot mutate bytes already advertised as StablePrefix.
func Pack(history []Message, budget int) PackResult {
	normalized := normalizeSystemMessages(history)
	anchor := firstUserIndex(history)
	kept := make([]Message, 0, len(history))
	stablePrefix := 0
	used := 0
	if anchor >= 0 {
		kept = append(kept, normalized[:anchor+1]...)
		used = messagesTokens(kept)
		stablePrefix = len(kept)
	}

	units := completePackingUnits(normalized, stablePrefix)
	selected := make([][]Message, 0, len(units))
	for i := len(units) - 1; i >= 0; i-- {
		cost := messagesTokens(units[i])
		if len(selected) > 0 && used+cost > budget {
			break
		}
		selected = append(selected, units[i])
		used += cost
	}
	slices.Reverse(selected)
	for _, unit := range selected {
		kept = append(kept, unit...)
	}
	// Defense in depth for malformed imported history. completePackingUnits
	// already emits paired groups, but these passes preserve the old contract.
	kept = dropDanglingToolCalls(kept)
	kept = dropOrphanTools(kept)
	return PackResult{
		Messages:     kept,
		Kept:         len(kept),
		StablePrefix: stablePrefix,
	}
}

func firstUserIndex(history []Message) int {
	for i := range history {
		if history[i].Role == RoleUser {
			return i
		}
	}
	return -1
}

func messagesTokens(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += message.Tokens()
	}
	return total
}

// completePackingUnits turns a tool round into an atomic packing unit. A
// partially answered parallel-call round is discarded whole: retaining either
// its ProviderState or one result without every sibling is rejected by strict
// provider APIs. Ordinary messages are single-message units.
func completePackingUnits(history []Message, skipBefore int) [][]Message {
	units := make([][]Message, 0, len(history))
	for i := 0; i < len(history); i++ {
		if i < skipBefore {
			continue
		}
		message := history[i]
		if message.Role == RoleTool {
			continue // orphaned or consumed with its owner below
		}
		if message.Role != RoleAssistant || len(message.ToolCalls) == 0 {
			units = append(units, []Message{message})
			continue
		}

		issued := map[string]bool{}
		complete := true
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				complete = false
			}
			issued[call.ID] = true
		}
		answers := map[string]bool{}
		results := make([]Message, 0, len(message.ToolCalls))
		j := i + 1
		for ; j < len(history) && history[j].Role == RoleTool; j++ {
			if issued[history[j].ToolCallID] {
				answers[history[j].ToolCallID] = true
				results = append(results, history[j])
			}
		}
		for _, call := range message.ToolCalls {
			if !answers[call.ID] {
				complete = false
				break
			}
		}
		if complete {
			units = append(units, append([]Message{message}, results...))
		}
		i = j - 1
	}
	return units
}

func normalizeSystemMessages(history []Message) []Message {
	out := slices.Clone(history)
	leading := true
	for i := range out {
		if out[i].Role == RoleSystem && leading {
			continue
		}
		leading = false
		if out[i].Role == RoleSystem {
			out[i].Role = RoleUser
		}
	}
	return out
}

// anchorUserMessage guarantees the packed window carries a user-role message
// whenever history has one. The newest-first walk drops oldest-first, so a long
// single turn (one task message, then dozens of assistant+tool rounds that fill
// the budget) evicts the sole user task and hands the backend a userless window,
// which 400s every OpenAI-compatible server ("no user query found in messages").
// When no user survived, recover the FIRST user message (the original task, the
// agent's anchor against drift), prepended chronologically over budget: the same
// deliberately-over-budget guarantee newestToolGroup and the always-keep-newest
// path already make. A lone user message carries no tool-call pairing, so this is
// safe after the dangling/orphan passes. No-op when a recent user already
// survived (normal multi-turn) or history has no user message at all.
func anchorUserMessage(kept, history []Message) []Message {
	for _, m := range kept {
		if m.Role == RoleUser {
			return kept
		}
	}
	for i := range history {
		if history[i].Role == RoleUser {
			return append([]Message{history[i]}, kept...)
		}
	}
	return kept
}

// demoteSystemMessages rewrites every system-role message in the packed history
// to a user message. buildMessages always prepends the embedded system prompt as
// wire element 0, so any system message Pack returns is a SECOND, non-leading
// system message, which strict OpenAI-compat backends reject outright ("System
// message must be at the beginning"; observed on strict backends like Ollama
// and llama.cpp), the same class of wire-shape 400 the dangling/orphan/userless passes
// guard against. The only system content reaching history is a soft-nudge note
// (the embedded prompt is never stored there), so demoting to user keeps that note,
// automated-check prefix and all, in front of the model while keeping the wire
// legal everywhere. Must run AFTER anchorUserMessage: a demoted nudge would
// otherwise masquerade as a surviving user message and suppress the original-task
// anchor. Mutates only the copied kept slice, never history.
func demoteSystemMessages(kept []Message) []Message {
	for i := range kept {
		if kept[i].Role == RoleSystem {
			kept[i].Role = RoleUser
		}
	}
	return kept
}

// newestToolGroup returns the assistant that issued the newest tool result
// together with every tool result answering it, chronologically: the minimal
// well-formed unit that honours "always keep the newest" when the newest
// history message is a tool result. nil when the newest message isn't an
// identifiable tool result (the budget walk already keeps non-tool newests) or
// no owning assistant exists (an unpairable tool can't be kept anyway).
func newestToolGroup(history []Message) []Message {
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role != RoleTool || last.ToolCallID == "" {
		return nil
	}
	owner := -1
search:
	for i := len(history) - 2; i >= 0; i-- {
		if history[i].Role != RoleAssistant {
			continue
		}
		for _, tc := range history[i].ToolCalls {
			if tc.ID == last.ToolCallID {
				owner = i
				break search
			}
		}
	}
	if owner < 0 {
		return nil
	}
	// Parallel tool calls put [assistant(c1,c2), tool(c1), tool(c2)] at the tail,
	// so collect every tool result whose id the owning assistant issued, not
	// just the immediately-preceding one.
	ids := map[string]bool{}
	for _, tc := range history[owner].ToolCalls {
		if tc.ID != "" {
			ids[tc.ID] = true
		}
	}
	group := []Message{history[owner]}
	for i := owner + 1; i < len(history); i++ {
		if history[i].Role == RoleTool && ids[history[i].ToolCallID] {
			group = append(group, history[i])
		}
	}
	return group
}

// dropOrphanTools removes tool messages whose tool_call_id has no matching
// assistant.tool_calls entry earlier in the slice: sending one alone 400s on
// every OpenAI-compatible backend ("tool message without preceding
// tool_calls").
//
// Empty IDs are orphans on both ends: otherwise one empty-id assistant call
// would let every empty-id tool message ride through seen[""], the exact 400
// we guard against. An unidentifiable tool message has no valid pairing.
func dropOrphanTools(kept []Message) []Message {
	seen := map[string]bool{}
	out := kept[:0]
	for _, m := range kept {
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					seen[tc.ID] = true
				}
			}
		}
		if m.Role == RoleTool && (m.ToolCallID == "" || !seen[m.ToolCallID]) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// dropDanglingToolCalls removes any assistant message whose tool_calls include
// an id with no answering tool message in the kept slice: the mirror of
// dropOrphanTools. An assistant.tool_calls followed by fewer tool results than
// calls issued 400s every OpenAI-compatible backend with "missing tool
// response". This shape is produced whenever a turn is aborted mid-tool: the
// TUI appends the assistant.tool_calls as soon as the round closes, but a Ctrl+C
// / stream-error / idle-stall then drops the pending calls so their tool results
// never arrive (see tui.endTurn). On the user's next request that dangling
// assistant would otherwise reach the wire and wedge the conversation until
// /clear. Empty ids count as unanswered: an unidentifiable call can't be paired.
func dropDanglingToolCalls(kept []Message) []Message {
	answered := map[string]bool{}
	for _, m := range kept {
		if m.Role == RoleTool && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	out := kept[:0]
	for _, m := range kept {
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			dangling := false
			for _, tc := range m.ToolCalls {
				if !answered[tc.ID] {
					dangling = true
					break
				}
			}
			if dangling {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// Budget subtracts the fixed reservations from the total context size, then
// leaves a headroom margin (see budgetHeadroomDivisor) so a char/4 undercount
// can't push the real prompt past the declared window.
func Budget(ctxSize int) int {
	b := ctxSize - FixedSystem - FixedTools - ResponseReserve(ctxSize)
	if b < 0 {
		return 0
	}
	return b - b/budgetHeadroomDivisor
}

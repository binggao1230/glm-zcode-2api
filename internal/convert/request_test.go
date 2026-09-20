package convert

import (
	"encoding/json"
	"testing"

	"glm-zcode-2api/internal/anthropic"
	"glm-zcode-2api/internal/openai"
)

func raw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	buf, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return buf
}

func defaultOptions() Options {
	return Options{DefaultMaxTokens: 128000, ThinkingEnabled: true, ThinkingEffort: "max", PromptCache: true}
}

func TestRequestShape(t *testing.T) {
	req := &openai.ChatRequest{
		Model: "glm-5.3",
		Messages: []openai.Message{
			{Role: "system", Content: raw(t, "be terse")},
			{Role: "developer", Content: raw(t, "prefer tools")},
			{Role: "user", Content: raw(t, "weather in Paris?")},
			{Role: "assistant", Content: raw(t, "checking"), ToolCalls: []openai.ToolCall{{
				ID: "call_1", Type: "function",
				Function: openai.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Paris"}`},
			}}},
			{Role: "tool", ToolCallID: "call_1", Content: raw(t, "18C")},
			{Role: "tool", ToolCallID: "call_1", Content: raw(t, "rain")},
		},
		Tools: []openai.Tool{{
			Type: "function",
			Function: openai.ToolFunction{
				Name: "get_weather", Description: "lookup",
				Parameters: raw(t, map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}),
			},
		}},
		ToolChoice: raw(t, "required"),
		Stream:     true,
	}

	out, err := Request(req, "GLM-5.3", defaultOptions())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	if out.Model != "GLM-5.3" || out.MaxTokens != 128000 || !out.Stream {
		t.Fatalf("unexpected head of request: %+v", out)
	}
	if len(out.System) != 2 {
		t.Fatalf("system blocks = %d, want 2 (system + developer)", len(out.System))
	}
	if out.System[0]["text"] != "be terse" || out.System[1]["text"] != "prefer tools" {
		t.Fatalf("system text mismatch: %+v", out.System)
	}
	if out.System[1]["cache_control"] == nil {
		t.Fatalf("expected a cache breakpoint on the system prompt: %+v", out.System)
	}

	if len(out.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, merged tool results): %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[1].Role != "assistant" || out.Messages[1].Content[1]["type"] != "tool_use" {
		t.Fatalf("assistant tool_use missing: %+v", out.Messages[1])
	}
	input, _ := out.Messages[1].Content[1]["input"].(map[string]any)
	if input["city"] != "Paris" {
		t.Fatalf("tool input not decoded: %+v", out.Messages[1].Content[1])
	}
	results := out.Messages[2]
	if results.Role != "user" || len(results.Content) != 2 {
		t.Fatalf("consecutive tool results must merge into one user turn: %+v", results)
	}
	for _, block := range results.Content {
		if block["type"] != "tool_result" || block["tool_use_id"] != "call_1" {
			t.Fatalf("unexpected tool_result block: %+v", block)
		}
	}

	if len(out.Tools) != 1 || out.Tools[0]["name"] != "get_weather" {
		t.Fatalf("tools not converted: %+v", out.Tools)
	}
	if out.Tools[0]["input_schema"] == nil {
		t.Fatalf("tool input_schema missing: %+v", out.Tools[0])
	}
	if out.ToolChoice["type"] != "any" {
		t.Fatalf("tool_choice required must map to any: %+v", out.ToolChoice)
	}
	if out.Thinking["type"] != "enabled" || out.OutputConfig["effort"] != "max" {
		t.Fatalf("reasoning controls missing: thinking=%+v output_config=%+v", out.Thinking, out.OutputConfig)
	}
}

func TestRequestToolChoiceNoneDropsTools(t *testing.T) {
	req := &openai.ChatRequest{
		Model:    "glm-5.3",
		Messages: []openai.Message{{Role: "user", Content: raw(t, "hi")}},
		Tools: []openai.Tool{{
			Type:     "function",
			Function: openai.ToolFunction{Name: "f", Parameters: raw(t, map[string]any{"type": "object"})},
		}},
		ToolChoice: raw(t, "none"),
	}
	out, err := Request(req, "GLM-5.3", defaultOptions())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(out.Tools) != 0 || out.ToolChoice != nil {
		t.Fatalf("tool_choice none must drop tools entirely: %+v", out.Tools)
	}
}

func TestRequestImageAndThinkingDisabled(t *testing.T) {
	opts := defaultOptions()
	opts.ThinkingEnabled = false
	req := &openai.ChatRequest{
		Model: "glm-5.3-flash",
		Messages: []openai.Message{{Role: "user", Content: raw(t, []map[string]any{
			{"type": "text", "text": "what is this"},
			{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}},
		})}},
		MaxTokens: intPtr(2048),
	}
	out, err := Request(req, "GLM-5.3-Flash", opts)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if out.MaxTokens != 2048 {
		t.Fatalf("max_tokens = %d, want the client value 2048", out.MaxTokens)
	}
	if kind, _ := out.Thinking["type"].(string); kind != "disabled" {
		t.Fatalf("disabled config must send thinking.type=disabled: %+v", out.Thinking)
	}
	if out.OutputConfig != nil {
		t.Fatalf("disabled thinking must not carry output_config: %+v", out.OutputConfig)
	}
	image := out.Messages[0].Content[1]
	source, _ := image["source"].(map[string]any)
	if image["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "AAAA" {
		t.Fatalf("image block not converted: %+v", image)
	}
}

func TestRequestRejectsUnknownRoleAndEmptyMessages(t *testing.T) {
	if _, err := Request(&openai.ChatRequest{}, "GLM-5.3", defaultOptions()); err == nil {
		t.Fatal("empty messages must be rejected")
	}
	req := &openai.ChatRequest{
		Messages: []openai.Message{
			{Role: "user", Content: raw(t, "hi")},
			{Role: "wizard", Content: raw(t, "boo")},
		},
	}
	if _, err := Request(req, "GLM-5.3", defaultOptions()); err == nil {
		t.Fatal("unknown role must be rejected")
	}
}

func TestReplayInjectsSignedThinking(t *testing.T) {
	cache := NewReplayCache(8)
	assistant := openai.Message{
		Role:    "assistant",
		Content: raw(t, "checking"),
		ToolCalls: []openai.ToolCall{{
			ID: "call_1", Type: "function",
			Function: openai.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Paris"}`},
		}},
	}
	blocks := []anthropic.Block{{"type": "thinking", "thinking": "hmm", "signature": "sig-1"}}
	cache.Put([]string{TurnKey("checking", assistant.ToolCalls)}, blocks)

	opts := defaultOptions()
	opts.Replay = cache
	req := &openai.ChatRequest{
		Messages: []openai.Message{
			{Role: "user", Content: raw(t, "weather?")},
			assistant,
			{Role: "tool", ToolCallID: "call_1", Content: raw(t, "18C")},
		},
	}
	out, err := Request(req, "GLM-5.3", opts)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	first := out.Messages[1].Content[0]
	if first["type"] != "thinking" || first["signature"] != "sig-1" {
		t.Fatalf("signed thinking block was not replayed: %+v", out.Messages[1].Content)
	}
}

func TestThinkingLevelMapping(t *testing.T) {
	cases := []struct {
		name       string
		request    openai.ChatRequest
		wantType   string
		wantEffort string
	}{
		{name: "config default", request: openai.ChatRequest{}},
		{name: "low", request: openai.ChatRequest{ReasoningEffort: "low"}, wantType: "enabled", wantEffort: "low"},
		{name: "minimal folds to low", request: openai.ChatRequest{ReasoningEffort: "minimal"}, wantType: "enabled", wantEffort: "low"},
		{name: "medium", request: openai.ChatRequest{ReasoningEffort: "medium"}, wantType: "enabled", wantEffort: "medium"},
		{name: "high", request: openai.ChatRequest{ReasoningEffort: "high"}, wantType: "enabled", wantEffort: "high"},
		{name: "xhigh folds to max", request: openai.ChatRequest{ReasoningEffort: "xhigh"}, wantType: "enabled", wantEffort: "max"},
		{name: "max", request: openai.ChatRequest{ReasoningEffort: "max"}, wantType: "enabled", wantEffort: "max"},
		{name: "off", request: openai.ChatRequest{ReasoningEffort: "off"}, wantType: "disabled"},
		{name: "reasoning object", request: openai.ChatRequest{Reasoning: raw(t, map[string]any{"effort": "high"})}, wantType: "enabled", wantEffort: "high"},
		{name: "unknown level keeps config", request: openai.ChatRequest{ReasoningEffort: "banana"}},
		{name: "thinking disabled wins", request: openai.ChatRequest{
			ReasoningEffort: "high",
			Thinking:        raw(t, map[string]any{"type": "disabled"}),
		}, wantType: "disabled"},
		{name: "thinking enabled only", request: openai.ChatRequest{
			Thinking: raw(t, map[string]any{"type": "enabled"}),
		}, wantType: "enabled", wantEffort: "max"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.request
			req.Messages = []openai.Message{{Role: "user", Content: raw(t, "hi")}}
			out, err := Request(&req, "GLM-5.3-Flash", defaultOptions())
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			wantType := tc.wantType
			if wantType == "" {
				wantType = "enabled"
			}
			if tc.wantEffort == "" && wantType == "enabled" {
				tc.wantEffort = "max"
			}
			if got, _ := out.Thinking["type"].(string); got != wantType {
				t.Fatalf("thinking.type = %q, want %q", got, wantType)
			}
			if wantType == "disabled" {
				if out.OutputConfig != nil {
					t.Fatalf("disabled thinking must not carry output_config: %+v", out.OutputConfig)
				}
				return
			}
			if got, _ := out.OutputConfig["effort"].(string); got != tc.wantEffort {
				t.Fatalf("output_config.effort = %q, want %q", got, tc.wantEffort)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

package convert

import (
	"encoding/json"
	"testing"

	"glm-zcode-2api/internal/anthropic"
	"glm-zcode-2api/internal/openai"
)

func event(t *testing.T, payload string) anthropic.Event {
	t.Helper()
	var ev anthropic.Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("decode event %s: %v", payload, err)
	}
	return ev
}

func TestTranslatorStreamsTextThinkingAndTools(t *testing.T) {
	translator := NewTranslator("glm-5.3", 1700000000)
	events := []string{
		`{"type":"message_start","message":{"id":"msg_abc","model":"GLM-5.3","usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":900}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me think"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-42"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Paris is"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" 18C"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_9","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":33}}`,
		`{"type":"message_stop"}`,
	}

	var chunks []openai.Chunk
	for _, payload := range events {
		produced, err := translator.Handle(event(t, payload))
		if err != nil {
			t.Fatalf("Handle(%s): %v", payload, err)
		}
		chunks = append(chunks, produced...)
	}

	if translator.ID != "chatcmpl-abc" {
		t.Fatalf("id = %q, want chatcmpl-abc", translator.ID)
	}
	var content, reasoning string
	var toolName, toolArgs string
	for _, chunk := range chunks {
		for _, choice := range chunk.Choices {
			content += choice.Delta.Content
			reasoning += choice.Delta.ReasoningContent
			for _, call := range choice.Delta.ToolCalls {
				if call.Function != nil {
					if call.Function.Name != "" {
						toolName = call.Function.Name
					}
					toolArgs += call.Function.Arguments
				}
			}
		}
	}
	if content != "Paris is 18C" {
		t.Fatalf("content = %q", content)
	}
	if reasoning != "let me think" {
		t.Fatalf("reasoning = %q", reasoning)
	}
	if toolName != "get_weather" || toolArgs != `{"city":"Paris"}` {
		t.Fatalf("tool call = %q %q", toolName, toolArgs)
	}
	if reason := translator.FinishReason(); reason != "tool_calls" {
		t.Fatalf("finish reason = %q", reason)
	}

	usage := translator.Usage()
	if usage.PromptTokens != 1000 || usage.CompletionTokens != 33 || usage.TotalTokens != 1033 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 900 {
		t.Fatalf("cached tokens not reported: %+v", usage.PromptTokensDetails)
	}

	keys, blocks := translator.Replay(translator.ToolCalls())
	if len(blocks) != 1 || blocks[0]["signature"] != "sig-42" {
		t.Fatalf("signed thinking block missing from replay payload: %+v", blocks)
	}
	cache := NewReplayCache(4)
	cache.Put(keys, blocks)
	echoed := openai.Message{
		Role:    "assistant",
		Content: json.RawMessage(`"Paris is 18C"`),
		ToolCalls: []openai.ToolCall{{
			ID: "call_9", Type: "function",
			Function: openai.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Paris"}`},
		}},
	}
	if got := cache.Lookup(echoed, echoed.Content); len(got) != 1 || got[0]["signature"] != "sig-42" {
		t.Fatalf("replay lookup by echoed turn failed: %+v", got)
	}
	// A client that echoes other arguments still matches by tool call id.
	loose := echoed
	loose.ToolCalls = []openai.ToolCall{{
		ID: "call_9", Type: "function",
		Function: openai.ToolCallFunction{Name: "get_weather", Arguments: `{"city": "Paris"}`},
	}}
	if got := cache.Lookup(loose, loose.Content); len(got) != 1 {
		t.Fatalf("replay lookup by tool id failed: %+v", got)
	}
}

func TestTranslatorAggregatesNonStreamingResponse(t *testing.T) {
	translator := NewTranslator("glm-5.3", 1700000000)
	for _, payload := range []string{
		`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12,"output_tokens":0}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	} {
		if _, err := translator.Handle(event(t, payload)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}
	response := translator.Response()
	if response.Object != "chat.completion" || len(response.Choices) != 1 {
		t.Fatalf("response head: %+v", response)
	}
	choice := response.Choices[0]
	if choice.Message.Content == nil || *choice.Message.Content != "hello world" {
		t.Fatalf("content: %+v", choice.Message.Content)
	}
	if choice.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", choice.FinishReason)
	}
	if response.Usage == nil || response.Usage.PromptTokens != 12 || response.Usage.CompletionTokens != 7 {
		t.Fatalf("usage: %+v", response.Usage)
	}
}

func TestTranslatorReportsStreamError(t *testing.T) {
	translator := NewTranslator("glm-5.3", 1700000000)
	_, err := translator.Handle(event(t, `{"type":"error","error":{"type":"rate_limit_error","code":"1310","message":"weekly limit reached"}}`))
	if err == nil {
		t.Fatal("upstream error event must surface as an error")
	}
	streamErr, ok := err.(*UpstreamError)
	if !ok || streamErr.Code != "1310" || streamErr.Message != "weekly limit reached" {
		t.Fatalf("error payload = %#v", err)
	}
}

func TestTranslatorMapsStopReasons(t *testing.T) {
	cases := map[string]string{"end_turn": "stop", "max_tokens": "length", "stop_sequence": "stop", "refusal": "content_filter", "tool_use": "tool_calls"}
	for upstream, want := range cases {
		translator := NewTranslator("glm-5.3", 0)
		if _, err := translator.Handle(event(t, `{"type":"message_delta","delta":{"stop_reason":"`+upstream+`"},"usage":{"output_tokens":1}}`)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if got := translator.FinishReason(); got != want {
			t.Fatalf("stop_reason %q mapped to %q, want %q", upstream, got, want)
		}
	}
}

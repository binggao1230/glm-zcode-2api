package convert

import (
	"encoding/json"
	"fmt"
	"strings"

	"glm-zcode-proxy/internal/anthropic"
	"glm-zcode-proxy/internal/openai"
)

// toolState accumulates one upstream tool_use block.
type toolState struct {
	openaiIndex int
	index       int // index reported by the upstream
	id          string
	name        string
	args        strings.Builder
}

// Translator turns the upstream Anthropic event stream into OpenAI chunks and
// the aggregated non-streaming response.
type Translator struct {
	ClientModel string
	ID          string
	Created     int64

	text      strings.Builder
	thinking  strings.Builder
	signature strings.Builder
	redacted  []anthropic.Block

	byBlock map[int]*toolState
	tools   []*toolState

	usage      anthropic.Usage
	stopReason string
	finished   bool
}

func NewTranslator(clientModel string, created int64) *Translator {
	return &Translator{
		ClientModel: clientModel,
		ID:          "chatcmpl-" + randomSuffix(),
		Created:     created,
		byBlock:     make(map[int]*toolState),
	}
}

// Handle consumes one upstream event and returns the chunks to send onward.
func (t *Translator) Handle(ev anthropic.Event) ([]openai.Chunk, error) {
	switch ev.Type {
	case "", "ping":
		return nil, nil

	case "message_start":
		if ev.Message != nil {
			if ev.Message.ID != "" {
				t.ID = "chatcmpl-" + strings.TrimPrefix(ev.Message.ID, "msg_")
			}
			t.mergeUsage(ev.Message.Usage)
		}
		return nil, nil

	case "content_block_start":
		if ev.ContentBlock == nil {
			return nil, nil
		}
		switch ev.ContentBlock.Type {
		case "text":
			if ev.ContentBlock.Text == "" {
				return nil, nil
			}
			t.text.WriteString(ev.ContentBlock.Text)
			return []openai.Chunk{t.chunk(openai.Delta{Content: ev.ContentBlock.Text})}, nil
		case "thinking":
			if ev.ContentBlock.Thinking == "" {
				return nil, nil
			}
			t.thinking.WriteString(ev.ContentBlock.Thinking)
			return []openai.Chunk{t.chunk(openai.Delta{ReasoningContent: ev.ContentBlock.Thinking})}, nil
		case "redacted_thinking":
			t.redacted = append(t.redacted, anthropic.Block{
				"type": "redacted_thinking", "data": ev.ContentBlock.Data,
			})
			return nil, nil
		case "tool_use":
			state := &toolState{
				index:       ev.Index,
				openaiIndex: len(t.tools),
				id:          ev.ContentBlock.ID,
				name:        ev.ContentBlock.Name,
			}
			if len(ev.ContentBlock.Input) > 0 {
				raw := strings.TrimSpace(string(ev.ContentBlock.Input))
				if raw != "" && raw != "{}" && raw != "null" {
					state.args.WriteString(raw)
				}
			}
			t.byBlock[ev.Index] = state
			t.tools = append(t.tools, state)
			return []openai.Chunk{t.chunk(openai.Delta{ToolCalls: []openai.ToolCallDelta{{
				Index: state.openaiIndex,
				ID:    state.id,
				Type:  "function",
				Function: &openai.ToolCallDeltaFn{
					Name:      state.name,
					Arguments: state.args.String(),
				},
			}}})}, nil
		}
		return nil, nil

	case "content_block_delta":
		var delta anthropic.BlockDelta
		if len(ev.Delta) > 0 {
			if err := json.Unmarshal(ev.Delta, &delta); err != nil {
				return nil, fmt.Errorf("decode content_block_delta: %w", err)
			}
		}
		switch delta.Type {
		case "text_delta":
			if delta.Text == "" {
				return nil, nil
			}
			t.text.WriteString(delta.Text)
			return []openai.Chunk{t.chunk(openai.Delta{Content: delta.Text})}, nil
		case "thinking_delta":
			if delta.Thinking == "" {
				return nil, nil
			}
			t.thinking.WriteString(delta.Thinking)
			return []openai.Chunk{t.chunk(openai.Delta{ReasoningContent: delta.Thinking})}, nil
		case "signature_delta":
			t.signature.WriteString(delta.Signature)
			return nil, nil
		case "input_json_delta":
			state := t.byBlock[ev.Index]
			if state == nil || delta.PartialJSON == "" {
				return nil, nil
			}
			state.args.WriteString(delta.PartialJSON)
			return []openai.Chunk{t.chunk(openai.Delta{ToolCalls: []openai.ToolCallDelta{{
				Index:    state.openaiIndex,
				Function: &openai.ToolCallDeltaFn{Arguments: delta.PartialJSON},
			}}})}, nil
		}
		return nil, nil

	case "message_delta":
		if len(ev.Delta) > 0 {
			var delta anthropic.BlockDelta
			if err := json.Unmarshal(ev.Delta, &delta); err != nil {
				return nil, fmt.Errorf("decode message_delta: %w", err)
			}
			if delta.StopReason != "" {
				t.stopReason = delta.StopReason
			}
		}
		t.mergeUsage(ev.Usage)
		return nil, nil

	case "message_stop":
		t.finished = true
		return nil, nil

	case "error":
		if ev.Error != nil {
			return nil, &UpstreamError{Type: ev.Error.Type, Message: ev.Error.Message, Code: ev.Error.Code}
		}
		return nil, fmt.Errorf("upstream reported an unspecified error")
	}
	return nil, nil
}

// RoleChunk is the opening chunk that announces the assistant role.
func (t *Translator) RoleChunk() openai.Chunk {
	return t.chunk(openai.Delta{Role: "assistant"})
}

// ToolCalls returns the accumulated tool calls of this turn.
func (t *Translator) ToolCalls() []openai.ToolCall {
	if len(t.tools) == 0 {
		return nil
	}
	calls := make([]openai.ToolCall, 0, len(t.tools))
	for _, tool := range t.tools {
		args := tool.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		calls = append(calls, openai.ToolCall{
			ID:       tool.id,
			Type:     "function",
			Function: openai.ToolCallFunction{Name: tool.name, Arguments: args},
		})
	}
	return calls
}

// FinalChunks emits the terminal chunk carrying the finish reason.
func (t *Translator) FinalChunks() []openai.Chunk {
	reason := t.FinishReason()
	return []openai.Chunk{t.chunk(openai.Delta{}, &reason)}
}

// FinishReason maps the upstream stop reason onto the OpenAI vocabulary.
func (t *Translator) FinishReason() string {
	switch t.stopReason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "refusal":
		return "content_filter"
	case "stop_sequence", "end_turn", "":
		if len(t.tools) > 0 {
			return "tool_calls"
		}
		return "stop"
	default:
		if len(t.tools) > 0 {
			return "tool_calls"
		}
		return "stop"
	}
}

// Usage renders the accumulated usage in the OpenAI shape.
func (t *Translator) Usage() openai.Usage {
	prompt := t.usage.InputTokens + t.usage.CacheReadInputTokens + t.usage.CacheCreationInputTokens
	usage := openai.Usage{
		PromptTokens:     prompt,
		CompletionTokens: t.usage.OutputTokens,
		TotalTokens:      prompt + t.usage.OutputTokens,
	}
	if t.usage.CacheReadInputTokens > 0 {
		usage.PromptTokensDetails = &openai.PromptTokensDetails{CachedTokens: t.usage.CacheReadInputTokens}
	}
	return usage
}

// Response builds the full non-streaming response body.
func (t *Translator) Response() openai.Response {
	reason := t.FinishReason()
	usage := t.Usage()
	message := openai.ResponseMessage{Role: "assistant"}
	text := t.text.String()
	switch {
	case text != "":
		message.Content = &text
	case len(t.tools) == 0 && t.thinking.Len() > 0:
		empty := ""
		message.Content = &empty
	default:
		empty := ""
		message.Content = &empty
	}
	if reasoning := t.thinking.String(); reasoning != "" {
		message.ReasoningContent = reasoning
	}
	for _, call := range t.ToolCalls() {
		message.ToolCalls = append(message.ToolCalls, openai.ResponseToolCall{
			ID:       call.ID,
			Type:     "function",
			Function: call.Function,
		})
	}
	return openai.Response{
		ID:      t.ID,
		Object:  "chat.completion",
		Created: t.Created,
		Model:   t.ClientModel,
		Choices: []openai.ResponseChoice{{
			Index:        0,
			Message:      message,
			FinishReason: reason,
		}},
		Usage: &usage,
	}
}

// Replay returns the cache keys and signed blocks needed to replay this turn.
// toolCalls identify the tool calls of the produced assistant message.
func (t *Translator) Replay(toolCalls []openai.ToolCall) ([]string, []anthropic.Block) {
	var blocks []anthropic.Block
	thinking := t.thinking.String()
	if thinking != "" && t.signature.Len() > 0 {
		blocks = append(blocks, anthropic.Block{
			"type":      "thinking",
			"thinking":  thinking,
			"signature": t.signature.String(),
		})
	}
	blocks = append(blocks, t.redacted...)
	if len(blocks) == 0 {
		return nil, nil
	}
	keys := []string{TurnKey(t.text.String(), toolCalls)}
	if thinking != "" {
		keys = append(keys, ThinkingKey(thinking))
	}
	if key := ToolIDsKey(toolCalls); key != "" {
		keys = append(keys, key)
	}
	return keys, blocks
}

func (t *Translator) mergeUsage(usage *anthropic.Usage) {
	if usage == nil {
		return
	}
	if usage.InputTokens > 0 {
		t.usage.InputTokens = usage.InputTokens
	}
	if usage.OutputTokens > 0 {
		t.usage.OutputTokens = usage.OutputTokens
	}
	if usage.CacheReadInputTokens > 0 {
		t.usage.CacheReadInputTokens = usage.CacheReadInputTokens
	}
	if usage.CacheCreationInputTokens > 0 {
		t.usage.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
}

func (t *Translator) chunk(delta openai.Delta, finish ...*string) openai.Chunk {
	choice := openai.ChunkChoice{Index: 0, Delta: delta}
	if len(finish) > 0 {
		choice.FinishReason = finish[0]
	}
	return openai.Chunk{
		ID:      t.ID,
		Object:  "chat.completion.chunk",
		Created: t.Created,
		Model:   t.ClientModel,
		Choices: []openai.ChunkChoice{choice},
	}
}

// UpstreamError is a structured error coming from the upstream service.
type UpstreamError struct {
	Status  int
	Type    string
	Message string
	Code    any
}

func (e *UpstreamError) Error() string {
	if e.Type == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

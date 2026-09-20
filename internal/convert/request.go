// Package convert translates between the OpenAI Chat Completions surface the
// gateway serves and the Anthropic Messages surface the upstream speaks.
package convert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"glm-zcode-2api/internal/anthropic"
	"glm-zcode-2api/internal/openai"
)

// Options controls request shaping.
type Options struct {
	DefaultMaxTokens int
	ThinkingEnabled  bool
	ThinkingEffort   string
	PromptCache      bool
	Replay           *ReplayCache
	// UserID is the account identifier sent as metadata.user_id when the
	// client does not provide one; mirrors what the ZCode app sends.
	UserID string
}

// maxCacheBreakpoints is the Anthropic limit for explicit cache breakpoints.
const maxCacheBreakpoints = 2

// Request converts an OpenAI chat request into an Anthropic Messages request.
// upstreamModel is the provider-side model name; clientModel is echoed back to
// the client in responses.
func Request(req *openai.ChatRequest, upstreamModel string, opts Options) (*anthropic.Request, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}

	out := &anthropic.Request{Model: upstreamModel, Stream: true}

	system, err := systemBlocks(req.Messages)
	if err != nil {
		return nil, err
	}
	out.System = system

	messages, err := chatMessages(req.Messages, opts)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages must contain at least one user or assistant turn")
	}
	out.Messages = messages

	if len(req.Tools) > 0 {
		tools, drop, err := tools(req.Tools, req.ToolChoice)
		if err != nil {
			return nil, err
		}
		if !drop {
			out.Tools = tools
			if choice := toolChoice(req.ToolChoice); choice != nil {
				out.ToolChoice = choice
			}
		}
	}

	out.MaxTokens = maxTokens(req, opts)
	out.Temperature = req.Temperature
	out.TopP = req.TopP
	out.StopSequences = stopSequences(req.Stop)
	if id := userID(req, opts.UserID); id != "" {
		out.Metadata = map[string]any{"user_id": id}
	}
	if enabled, effort := resolveThinking(req, opts); enabled {
		out.Thinking = map[string]any{"type": "enabled"}
		out.OutputConfig = map[string]any{"effort": effort}
	} else {
		out.Thinking = map[string]any{"type": "disabled"}
	}
	if opts.PromptCache {
		applyCacheControl(out)
	}
	return out, nil
}

// resolveThinking decides whether extended thinking is on and at which effort.
// The client wins when it asks for a level; the configuration is the default.
// The upstream accepts any effort string, so the mapping only normalizes the
// client vocabulary onto the levels ZCode itself uses (low/medium/high/max).
func resolveThinking(req *openai.ChatRequest, opts Options) (bool, string) {
	disabled, forced := thinkingDirective(req.Thinking)
	if disabled {
		return false, ""
	}
	level := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	if level == "" {
		level = strings.ToLower(strings.TrimSpace(reasoningEffortObject(req.Reasoning)))
	}
	switch level {
	case "none", "off", "disabled":
		return false, ""
	case "minimal":
		return true, "low"
	case "low", "medium", "high", "max":
		return true, level
	case "xhigh":
		return true, "max"
	case "", "auto":
		if forced {
			return true, opts.ThinkingEffort
		}
		return opts.ThinkingEnabled, opts.ThinkingEffort
	default:
		return opts.ThinkingEnabled, opts.ThinkingEffort
	}
}

// thinkingDirective reads Anthropic-style thinking controls.
func thinkingDirective(raw json.RawMessage) (disabled, enabled bool) {
	if len(raw) == 0 {
		return false, false
	}
	var object struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return false, false
	}
	switch object.Type {
	case "disabled", "off", "none":
		return true, false
	case "enabled":
		return false, true
	}
	return false, false
}

// reasoningEffortObject reads the OpenAI-style reasoning.effort control.
func reasoningEffortObject(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var object struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	return object.Effort
}

func maxTokens(req *openai.ChatRequest, opts Options) int {
	switch {
	case req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0:
		return *req.MaxCompletionTokens
	case req.MaxTokens != nil && *req.MaxTokens > 0:
		return *req.MaxTokens
	case opts.DefaultMaxTokens > 0:
		return opts.DefaultMaxTokens
	default:
		return 128000
	}
}

func userID(req *openai.ChatRequest, fallback string) string {
	if req.User != "" {
		return req.User
	}
	if v, ok := req.Metadata["user_id"].(string); ok && v != "" {
		return v
	}
	return fallback
}

func stopSequences(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if one == "" {
			return nil
		}
		return []string{one}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	return nil
}

// systemBlocks collects system and developer messages into the top-level
// system array. Anthropic has no developer role.
func systemBlocks(msgs []openai.Message) ([]anthropic.Block, error) {
	var blocks []anthropic.Block
	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
		default:
			continue
		}
		text, err := textContent(m.Content)
		if err != nil {
			return nil, fmt.Errorf("system message: %w", err)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		blocks = append(blocks, anthropic.Block{"type": "text", "text": text})
	}
	return blocks, nil
}

// chatMessages converts the conversational turns. Consecutive turns with the
// same effective role are merged because the upstream expects alternating
// user/assistant messages, and tool results must ride inside a user turn.
func chatMessages(msgs []openai.Message, opts Options) ([]anthropic.Message, error) {
	var out []anthropic.Message

	appendBlocks := func(role string, blocks []anthropic.Block) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, anthropic.Message{Role: role, Content: blocks})
	}

	for _, m := range msgs {
		switch m.Role {
		case "system", "developer":
			continue // handled as top-level system blocks
		case "user":
			blocks, err := userBlocks(m)
			if err != nil {
				return nil, err
			}
			appendBlocks("user", blocks)
		case "assistant":
			blocks, err := assistantBlocks(m, opts)
			if err != nil {
				return nil, err
			}
			appendBlocks("assistant", blocks)
		case "tool", "function":
			blocks, err := toolResultBlocks(m)
			if err != nil {
				return nil, err
			}
			appendBlocks("user", blocks)
		default:
			return nil, fmt.Errorf("unsupported message role %q", m.Role)
		}
	}
	return out, nil
}

func userBlocks(m openai.Message) ([]anthropic.Block, error) {
	if len(m.Content) == 0 || string(m.Content) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		if text == "" {
			return nil, nil
		}
		return []anthropic.Block{{"type": "text", "text": text}}, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return nil, fmt.Errorf("user content: %w", err)
	}
	blocks := make([]anthropic.Block, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text":
			if p.Text == "" {
				continue
			}
			blocks = append(blocks, anthropic.Block{"type": "text", "text": p.Text})
		case "image_url", "input_image":
			block, err := imageBlock(p)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		default:
			return nil, fmt.Errorf("unsupported content part %q", p.Type)
		}
	}
	return blocks, nil
}

func toolResultBlocks(m openai.Message) ([]anthropic.Block, error) {
	if m.ToolCallID == "" {
		return nil, fmt.Errorf("tool message without tool_call_id")
	}
	content, err := textContent(m.Content)
	if err != nil {
		return nil, fmt.Errorf("tool message: %w", err)
	}
	return []anthropic.Block{{
		"type":        "tool_result",
		"tool_use_id": m.ToolCallID,
		"content":     []anthropic.Block{{"type": "text", "text": content}},
	}}, nil
}

func assistantBlocks(m openai.Message, opts Options) ([]anthropic.Block, error) {
	var blocks []anthropic.Block

	// Replay the signed thinking block when this assistant turn is one the
	// gateway produced earlier; the upstream requires it for tool loops.
	if opts.Replay != nil {
		if cached := opts.Replay.Lookup(m, m.Content); len(cached) > 0 {
			blocks = append(blocks, cached...)
		}
	}

	if len(m.Content) > 0 && string(m.Content) != "null" {
		var text string
		if err := json.Unmarshal(m.Content, &text); err == nil {
			if text != "" {
				blocks = append(blocks, anthropic.Block{"type": "text", "text": text})
			}
		} else {
			var parts []contentPart
			if err := json.Unmarshal(m.Content, &parts); err != nil {
				return nil, fmt.Errorf("assistant content: %w", err)
			}
			for _, p := range parts {
				if p.Type == "text" && p.Text != "" {
					blocks = append(blocks, anthropic.Block{"type": "text", "text": p.Text})
				}
			}
		}
	}

	for _, call := range m.ToolCalls {
		input := map[string]any{}
		args := strings.TrimSpace(call.Function.Arguments)
		if args != "" {
			if err := json.Unmarshal([]byte(args), &input); err != nil {
				// Keep the loop alive: pass the raw text as a single value.
				input = map[string]any{"_raw": args}
			}
		}
		id := call.ID
		if id == "" {
			id = syntheticToolID(call.Function.Name, args)
		}
		blocks = append(blocks, anthropic.Block{
			"type":  "tool_use",
			"id":    id,
			"name":  call.Function.Name,
			"input": input,
		})
	}
	return blocks, nil
}

func syntheticToolID(name, args string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + args))
	return "call_" + hex.EncodeToString(sum[:12])
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

func imageBlock(p contentPart) (anthropic.Block, error) {
	if p.ImageURL == nil || p.ImageURL.URL == "" {
		return nil, fmt.Errorf("image part without url")
	}
	raw := p.ImageURL.URL
	if strings.HasPrefix(raw, "data:") {
		mediaType, data, err := parseDataURL(raw)
		if err != nil {
			return nil, err
		}
		return anthropic.Block{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": mediaType, "data": data,
		}}, nil
	}
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return anthropic.Block{"type": "image", "source": map[string]any{
			"type": "url", "url": raw,
		}}, nil
	}
	// Bare base64 payloads are accepted as PNG by convention.
	return anthropic.Block{"type": "image", "source": map[string]any{
		"type": "base64", "media_type": "image/png", "data": raw,
	}}, nil
}

func parseDataURL(raw string) (string, string, error) {
	rest := strings.TrimPrefix(raw, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", "", fmt.Errorf("malformed data URL")
	}
	mediaType := rest[:comma]
	if idx := strings.Index(mediaType, ";"); idx >= 0 {
		mediaType = mediaType[:idx]
	}
	if mediaType == "" {
		mediaType = "image/png"
	}
	data := rest[comma+1:]
	if strings.Contains(data, ";base64") {
		data = strings.TrimPrefix(data, ";base64")
	}
	return mediaType, data, nil
}

// tools converts OpenAI function tools. drop is true when tool_choice denies
// tool use entirely, in which case the tools must not be sent at all.
func tools(in []openai.Tool, choice json.RawMessage) ([]map[string]any, bool, error) {
	out := make([]map[string]any, 0, len(in))
	for _, t := range in {
		if t.Type != "" && t.Type != "function" {
			return nil, false, fmt.Errorf("unsupported tool type %q", t.Type)
		}
		if t.Function.Name == "" {
			return nil, false, fmt.Errorf("tool without a function name")
		}
		tool := map[string]any{"name": t.Function.Name}
		if t.Function.Description != "" {
			tool["description"] = t.Function.Description
		}
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		if len(t.Function.Parameters) > 0 {
			if err := json.Unmarshal(t.Function.Parameters, &schema); err != nil {
				return nil, false, fmt.Errorf("tool %q parameters: %w", t.Function.Name, err)
			}
		}
		tool["input_schema"] = schema
		out = append(out, tool)
	}
	if isToolChoiceNone(choice) {
		return nil, true, nil
	}
	return out, false, nil
}

func isToolChoiceNone(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && s == "none"
}

func toolChoice(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			return nil
		default:
			return nil
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Type == "function" && obj.Function.Name != "" {
			return map[string]any{"type": "tool", "name": obj.Function.Name}
		}
		if obj.Type == "auto" || obj.Type == "any" {
			return map[string]any{"type": obj.Type}
		}
	}
	return nil
}

// applyCacheControl marks the system prompt for prompt caching, mirroring what
// the ZCode client itself does. At most maxCacheBreakpoints are used.
func applyCacheControl(req *anthropic.Request) {
	for i := len(req.System) - 1; i >= 0 && i >= len(req.System)-maxCacheBreakpoints; i-- {
		if req.System[i]["text"] == "" {
			continue
		}
		req.System[i]["cache_control"] = map[string]any{"type": "ephemeral"}
	}
}

func textContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("unexpected content shape")
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "input_text" {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}

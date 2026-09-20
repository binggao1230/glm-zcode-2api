// Package anthropic holds the Anthropic Messages wire types used towards the
// upstream service and the SSE event shapes it emits.
package anthropic

import "encoding/json"

type Request struct {
	Model         string           `json:"model"`
	MaxTokens     int              `json:"max_tokens"`
	System        []Block          `json:"system,omitempty"`
	Messages      []Message        `json:"messages"`
	Tools         []map[string]any `json:"tools,omitempty"`
	ToolChoice    map[string]any   `json:"tool_choice,omitempty"`
	Thinking      map[string]any   `json:"thinking,omitempty"`
	OutputConfig  map[string]any   `json:"output_config,omitempty"`
	Metadata      map[string]any   `json:"metadata,omitempty"`
	Stream        bool             `json:"stream"`
	Temperature   *float64         `json:"temperature,omitempty"`
	TopP          *float64         `json:"top_p,omitempty"`
	StopSequences []string         `json:"stop_sequences,omitempty"`
}

type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Block is one content block; the fields used depend on Type.
type Block map[string]any

type Event struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	Message      *MessageStart   `json:"message"`
	ContentBlock *ContentBlock   `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
	Usage        *Usage          `json:"usage"`
	Error        *ErrorBody      `json:"error"`
}

type MessageStart struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage *Usage `json:"usage"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	Input     json.RawMessage `json:"input"`
}

type BlockDelta struct {
	Type         string  `json:"type"`
	Text         string  `json:"text"`
	Thinking     string  `json:"thinking"`
	Signature    string  `json:"signature"`
	PartialJSON  string  `json:"partial_json"`
	StopReason   string  `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence"`
}

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type ErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    any    `json:"code"`
}

// ErrorEnvelope is the JSON body the upstream returns for failed requests.
type ErrorEnvelope struct {
	Type  string     `json:"type"`
	Error *ErrorBody `json:"error"`
}

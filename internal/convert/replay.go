package convert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"glm-zcode-2api/internal/anthropic"
	"glm-zcode-2api/internal/openai"
)

// ReplayCache keeps the signed thinking blocks the upstream returned so they
// can be replayed on the next turn. Anthropic-protocol servers require the
// assistant thinking block to survive tool loops; OpenAI clients only echo the
// visible text, so the gateway remembers the signature itself.
type ReplayCache struct {
	mu      sync.Mutex
	entries map[string][]anthropic.Block
	order   []string
	limit   int
}

func NewReplayCache(limit int) *ReplayCache {
	if limit <= 0 {
		limit = 512
	}
	return &ReplayCache{entries: make(map[string][]anthropic.Block), limit: limit}
}

// Put stores blocks under every key that may identify the same assistant turn.
func (c *ReplayCache) Put(keys []string, blocks []anthropic.Block) {
	if c == nil || len(blocks) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, exists := c.entries[key]; !exists {
			c.order = append(c.order, key)
		}
		c.entries[key] = blocks
	}
	for len(c.order) > c.limit {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

// Lookup returns cached thinking blocks for an assistant turn. The turn key
// matches a verbatim echo of an earlier response, the tool key matches an echo
// that dropped the text, and the thinking key matches reasoning_content.
func (c *ReplayCache) Lookup(m openai.Message, content json.RawMessage) []anthropic.Block {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if blocks, ok := c.entries[AssistantKey(m, content)]; ok {
		return cloneBlocks(blocks)
	}
	if key := ToolKey(m); key != "" {
		if blocks, ok := c.entries[key]; ok {
			return cloneBlocks(blocks)
		}
	}
	if m.ReasoningContent != "" {
		if blocks, ok := c.entries[ThinkingKey(m.ReasoningContent)]; ok {
			return cloneBlocks(blocks)
		}
	}
	return nil
}

// Len reports the number of cached entries; used by the status endpoint.
func (c *ReplayCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func cloneBlocks(blocks []anthropic.Block) []anthropic.Block {
	out := make([]anthropic.Block, len(blocks))
	copy(out, blocks)
	return out
}

// AssistantKey identifies an assistant turn by its visible text and tool calls.
func AssistantKey(m openai.Message, content json.RawMessage) string {
	text, _ := textContent(content)
	return TurnKey(text, m.ToolCalls)
}

// TurnKey identifies an assistant turn by visible text and tool calls.
func TurnKey(text string, toolCalls []openai.ToolCall) string {
	var b strings.Builder
	b.WriteString("assistant\x00")
	b.WriteString(text)
	for _, call := range toolCalls {
		b.WriteString("\x00")
		b.WriteString(call.ID)
		b.WriteString("\x00")
		b.WriteString(call.Function.Name)
		b.WriteString("\x00")
		b.WriteString(strings.TrimSpace(call.Function.Arguments))
	}
	return hashKey("msg", b.String())
}

// ThinkingKey identifies a thinking block by its reasoning text.
func ThinkingKey(thinking string) string {
	return hashKey("think", thinking)
}

// ToolKey identifies an assistant tool-call turn by tool call ids alone; some
// clients drop the assistant text when replaying.
func ToolKey(m openai.Message) string {
	return ToolIDsKey(m.ToolCalls)
}

// ToolIDsKey identifies tool calls by their ids.
func ToolIDsKey(toolCalls []openai.ToolCall) string {
	if len(toolCalls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("tools\x00")
	for _, call := range toolCalls {
		b.WriteString(call.ID)
		b.WriteString("\x00")
	}
	return hashKey("tools", b.String())
}

func hashKey(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + ":" + hex.EncodeToString(sum[:16])
}

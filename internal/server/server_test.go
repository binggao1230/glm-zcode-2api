package server

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"glm-zcode-proxy/internal/config"
)

type capture struct {
	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header
}

func (c *capture) record(r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, body)
	c.headers = append(c.headers, r.Header.Clone())
}

func (c *capture) last(t *testing.T) (map[string]any, http.Header) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		t.Fatal("upstream received no requests")
	}
	return c.requests[len(c.requests)-1], c.headers[len(c.headers)-1]
}

// fakeUpstream serves a canned SSE script and records what the gateway sent.
func fakeUpstream(t *testing.T, sse string) (*httptest.Server, *capture) {
	t.Helper()
	rec := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sse)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)
	return server, rec
}

func newGateway(t *testing.T, upstreamURL string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	zcodeConfig := filepath.Join(dir, "config.json")
	document := `{"provider":{"builtin:bigmodel-coding-plan":{"name":"BigModel - Coding Plan","kind":"anthropic","enabled":true,"options":{"apiKey":"upstream-key","baseURL":"` + upstreamURL + `"}}}}`
	if err := os.WriteFile(zcodeConfig, []byte(document), 0o600); err != nil {
		t.Fatalf("write Z Code config: %v", err)
	}
	cfg := config.Default()
	cfg.APIKey = "local-key"
	cfg.Upstream.CredentialConfigPath = zcodeConfig
	return New(cfg, log.New(io.Discard, "", 0)).Handler()
}

const sseScript = `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","model":"GLM-5.3","usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"considering"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-7"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"sunny"}}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_1","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

func chatBody(stream bool) string {
	if stream {
		return `{"model":"glm-5.3","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"weather?"}]}`
	}
	return `{"model":"glm-5.3","messages":[{"role":"user","content":"weather?"}]}`
}

func post(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer local-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func ssePayloads(t *testing.T, body string) []map[string]any {
	t.Helper()
	var payloads []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("bad SSE payload %q: %v", data, err)
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func TestAuthentication(t *testing.T) {
	upstream, _ := fakeUpstream(t, sseScript)
	handler := newGateway(t, upstream.URL)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody(true)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody(true)))
	req.Header.Set("x-api-key", "wrong")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", rec.Code)
	}
}

func TestStreamingChat(t *testing.T) {
	upstream, rec := fakeUpstream(t, sseScript)
	handler := newGateway(t, upstream.URL)

	res := post(t, handler, chatBody(true))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if ct := res.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.HasSuffix(res.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("stream must end with [DONE]: %q", tail(res.Body.String(), 80))
	}

	payloads := ssePayloads(t, res.Body.String())
	if len(payloads) < 6 {
		t.Fatalf("expected several chunks, got %d", len(payloads))
	}
	first := payloads[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if first["role"] != "assistant" {
		t.Fatalf("first chunk must announce the role: %+v", first)
	}

	var content, reasoning, toolArgs, toolName string
	var finish string
	for _, payload := range payloads {
		choices := payload["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if v, ok := delta["content"].(string); ok {
				content += v
			}
			if v, ok := delta["reasoning_content"].(string); ok {
				reasoning += v
			}
			if calls, ok := delta["tool_calls"].([]any); ok {
				for _, raw := range calls {
					call := raw.(map[string]any)
					if fn, ok := call["function"].(map[string]any); ok {
						if v, ok := fn["name"].(string); ok {
							toolName = v
						}
						if v, ok := fn["arguments"].(string); ok {
							toolArgs += v
						}
					}
				}
			}
		}
		if reason, ok := choice["finish_reason"].(string); ok {
			finish = reason
		}
	}
	if content != "sunny" || reasoning != "considering" {
		t.Fatalf("content = %q reasoning = %q", content, reasoning)
	}
	if toolName != "get_weather" || toolArgs != `{"city":"Paris"}` {
		t.Fatalf("tool call = %q %q", toolName, toolArgs)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish_reason = %q", finish)
	}

	last := payloads[len(payloads)-1]
	usage, ok := last["usage"].(map[string]any)
	if !ok {
		t.Fatalf("include_usage must produce a final usage chunk: %+v", last)
	}
	if usage["prompt_tokens"].(float64) != 10 || usage["completion_tokens"].(float64) != 5 {
		t.Fatalf("usage = %+v", usage)
	}

	body, headers := rec.last(t)
	if headers.Get("x-api-key") != "upstream-key" || headers.Get("anthropic-version") == "" {
		t.Fatalf("upstream auth headers missing: %+v", headers)
	}
	if body["stream"] != true || body["model"] != "GLM-5.3" {
		t.Fatalf("upstream request body: %+v", body)
	}
	if body["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatalf("thinking not enabled upstream: %+v", body["thinking"])
	}
}

func TestNonStreamingChatAggregates(t *testing.T) {
	upstream, _ := fakeUpstream(t, sseScript)
	handler := newGateway(t, upstream.URL)

	res := post(t, handler, chatBody(false))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if ct := res.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	var payload map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["object"] != "chat.completion" {
		t.Fatalf("object = %v", payload["object"])
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "sunny" || message["reasoning_content"] != "considering" {
		t.Fatalf("message = %+v", message)
	}
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %+v", choice["finish_reason"])
	}
	calls := message["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "call_1" {
		t.Fatalf("tool calls = %+v", calls)
	}
}

func TestReplayAcrossTurns(t *testing.T) {
	upstream, rec := fakeUpstream(t, sseScript)
	handler := newGateway(t, upstream.URL)

	if res := post(t, handler, chatBody(true)); res.Code != http.StatusOK {
		t.Fatalf("first turn failed: %d %s", res.Code, res.Body.String())
	}

	followUp := `{"model":"glm-5.3","messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":"sunny","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"18C"}
	]}`
	if res := post(t, handler, followUp); res.Code != http.StatusOK {
		t.Fatalf("second turn failed: %d %s", res.Code, res.Body.String())
	}

	body, _ := rec.last(t)
	messages := body["messages"].([]any)
	assistant := messages[1].(map[string]any)
	blocks := assistant["content"].([]any)
	if len(blocks) == 0 || blocks[0].(map[string]any)["type"] != "thinking" {
		t.Fatalf("signed thinking block was not replayed: %+v", blocks)
	}
	if blocks[0].(map[string]any)["signature"] != "sig-7" {
		t.Fatalf("signature mismatch: %+v", blocks[0])
	}
}

func TestUpstreamQuotaErrorIsPassedThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","code":"1310","message":"[1310] weekly limit reached, resets 2026-09-21 23:57:54"}}`)
	}))
	defer upstream.Close()
	handler := newGateway(t, upstream.URL)

	res := post(t, handler, chatBody(true))
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	errBody := payload["error"].(map[string]any)
	if errBody["type"] != "rate_limit_error" || !strings.Contains(errBody["message"].(string), "1310") {
		t.Fatalf("error body = %+v", errBody)
	}

	// The same failure inside an already-open stream must be reported as an
	// error frame, not as a silently truncated success.
	streamUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_x\",\"usage\":{\"input_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n")
	}))
	defer streamUpstream.Close()
	handler = newGateway(t, streamUpstream.URL)
	res = post(t, handler, chatBody(true))
	if res.Code != http.StatusOK {
		t.Fatalf("stream status = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"error"`) {
		t.Fatalf("mid-stream error not surfaced: %s", res.Body.String())
	}
}

func TestModelsHealthAndUnknownModel(t *testing.T) {
	upstream, _ := fakeUpstream(t, sseScript)
	handler := newGateway(t, upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "glm-5.3-flash") {
		t.Fatalf("models: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"service":"glm-zcode-proxy"`) {
		t.Fatalf("healthz: %d %s", rec.Code, rec.Body.String())
	}

	res := post(t, handler, `{"model":"nope","messages":[{"role":"user","content":"hi"}]}`)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown model status = %d", res.Code)
	}
}

func TestHealthReportsMissingCredential(t *testing.T) {
	cfg := config.Default()
	cfg.Upstream.CredentialConfigPath = filepath.Join(t.TempDir(), "absent.json")
	handler := New(cfg, log.New(io.Discard, "", 0)).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"service":"glm-zcode-proxy"`) {
		t.Fatalf("health payload must identify the service: %s", rec.Body.String())
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// Package server exposes the OpenAI-compatible HTTP surface.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"glm-zcode-2api/internal/anthropic"
	"glm-zcode-2api/internal/config"
	"glm-zcode-2api/internal/convert"
	"glm-zcode-2api/internal/credential"
	"glm-zcode-2api/internal/openai"
	"glm-zcode-2api/internal/upstream"
)

// Service identifies this gateway in health responses.
const Service = "glm-zcode-2api"

type Server struct {
	cfg       *config.Config
	deviceID  string
	sessionID string
	resolver  *credential.Resolver
	replay    *convert.ReplayCache
	client    *upstream.Client
	logger    *log.Logger
	started   time.Time
}

func New(cfg *config.Config, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	deviceID := cfg.Upstream.DeviceID
	if deviceID == "" {
		deviceID = newUUID()
	}
	return &Server{
		cfg:       cfg,
		deviceID:  deviceID,
		sessionID: newUUID(),
		resolver: &credential.Resolver{
			ConfigPath: cfg.Upstream.CredentialConfigPath,
			ProviderID: cfg.Upstream.ProviderID,
		},
		replay: convert.NewReplayCache(512),
		client: &upstream.Client{
			BaseURL:     cfg.Upstream.BaseURL,
			APIKey:      cfg.Upstream.APIKey,
			APIVersion:  cfg.Upstream.AnthropicVersion,
			UserAgent:   cfg.Upstream.UserAgent,
			Beta:        cfg.Upstream.Beta,
			IdleTimeout: cfg.Upstream.IdleTimeout(),
			HTTP:        upstream.NewHTTPClient(cfg.Upstream.HeaderTimeout(), 16),
		},
		logger:  logger,
		started: time.Now(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/status", s.auth(s.handleStatus))
	mux.HandleFunc("/v1/models", s.auth(s.handleModels))
	mux.HandleFunc("/v1/chat/completions", s.auth(s.handleChat))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{
		"service": Service,
		"healthy": false,
		"models":  len(s.cfg.Models),
	}
	cred, err := s.resolver.Resolve()
	if err != nil {
		payload["error"] = err.Error()
		writeJSON(w, http.StatusServiceUnavailable, payload)
		return
	}
	payload["healthy"] = true
	payload["provider"] = cred.ProviderID
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cred, err := s.resolver.Resolve()
	payload := map[string]any{
		"service":          Service,
		"listen":           s.cfg.Listen,
		"uptime_seconds":   int(time.Since(s.started).Seconds()),
		"auth_required":    s.cfg.APIKey != "",
		"models":           s.cfg.ModelIDs(),
		"thinking_enabled": s.cfg.Thinking.Enabled,
		"thinking_effort":  s.cfg.Thinking.Effort,
		"replay_entries":   s.replay.Len(),
	}
	if err != nil {
		payload["credential"] = map[string]any{"available": false, "error": err.Error()}
		writeJSON(w, http.StatusServiceUnavailable, payload)
		return
	}
	payload["credential"] = map[string]any{
		"available":   true,
		"provider_id": cred.ProviderID,
		"provider":    cred.Provider,
		"base_url":    cred.BaseURL,
		"source":      cred.Source,
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	created := s.started.Unix()
	list := openai.ModelList{Object: "list", Data: make([]openai.ModelCard, 0, len(s.cfg.Models))}
	for _, m := range s.cfg.Models {
		list.Data = append(list.Data, openai.ModelCard{
			ID: m.ID, Object: "model", Created: created, OwnedBy: "zcode",
		})
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "POST is required", nil)
		return
	}

	body := http.MaxBytesReader(w, r.Body, int64(s.cfg.Server.MaxBodyMB)<<20)
	var req openai.ChatRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_body_too_large",
				fmt.Sprintf("request body exceeds %d MB", s.cfg.Server.MaxBodyMB), nil)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body: "+err.Error(), nil)
		return
	}

	spec, ok := s.cfg.Model(req.Model)
	if !ok {
		writeError(w, http.StatusNotFound, "model_not_found",
			fmt.Sprintf("model %q is not served by this gateway; available: %s",
				req.Model, strings.Join(s.cfg.ModelIDs(), ", ")), "model_not_found")
		return
	}

	cred, err := s.resolver.Resolve()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_credential_unavailable", err.Error(), nil)
		return
	}

	upstreamReq, err := convert.Request(&req, spec.Upstream, convert.Options{
		DefaultMaxTokens: spec.MaxOutputTokens,
		ThinkingEnabled:  s.cfg.Thinking.Enabled,
		ThinkingEffort:   s.cfg.Thinking.Effort,
		PromptCache:      s.cfg.Thinking.PromptCache,
		Replay:           s.replay,
		DeviceID:         mimicDeviceID(s.cfg.Upstream.MimicClient, s.deviceID),
		SessionID:        s.sessionID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), nil)
		return
	}

	client := s.clientFor(cred)
	translator := convert.NewTranslator(spec.ID, start.Unix())
	thinking := thinkingLabel(upstreamReq)
	upstreamURL := upstream.MessagesURL(client.BaseURL, client.GatewayOrigin)

	if !req.Stream {
		err = client.Messages(r.Context(), upstreamReq, upstream.Handlers{
			OnEvent: func(event anthropic.Event) error {
				_, handleErr := translator.Handle(event)
				return handleErr
			},
		})
		if err != nil {
			status, body := s.errorFor(err)
			s.logChat(spec.ID, false, status, start, translator, thinking, upstreamURL, err)
			writeError(w, status, body.Type, body.Message, body.Code)
			return
		}
		s.storeReplay(translator)
		writeJSON(w, http.StatusOK, translator.Response())
		s.logChat(spec.ID, false, http.StatusOK, start, translator, thinking, upstreamURL, nil)
		return
	}

	controller := http.NewResponseController(w)
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	started := false
	writeEvent := func(payload any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return err
		}
		return controller.Flush()
	}

	err = client.Messages(r.Context(), upstreamReq, upstream.Handlers{
		OnStart: func() error {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			started = true
			return writeEvent(translator.RoleChunk())
		},
		OnEvent: func(event anthropic.Event) error {
			chunks, err := translator.Handle(event)
			if err != nil {
				return err
			}
			for _, chunk := range chunks {
				if err := writeEvent(chunk); err != nil {
					return err
				}
			}
			return nil
		},
	})

	if !started {
		if err == nil {
			err = errors.New("upstream closed the stream before it started")
		}
		status, body := s.errorFor(err)
		s.logChat(spec.ID, true, status, start, translator, thinking, upstreamURL, err)
		writeError(w, status, body.Type, body.Message, body.Code)
		return
	}

	if err != nil && !errors.Is(err, context.Canceled) {
		_, body := s.errorFor(err)
		_ = writeEvent(openai.ErrorResponse{Error: body})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		_ = controller.Flush()
		s.logChat(spec.ID, true, s.statusFor(err), start, translator, thinking, upstreamURL, err)
		return
	}

	for _, chunk := range translator.FinalChunks() {
		if writeErr := writeEvent(chunk); writeErr != nil {
			break
		}
	}
	if includeUsage {
		usage := translator.Usage()
		_ = writeEvent(openai.Chunk{
			ID: translator.ID, Object: "chat.completion.chunk", Created: translator.Created,
			Model: spec.ID, Choices: []openai.ChunkChoice{}, Usage: &usage,
		})
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	_ = controller.Flush()
	s.storeReplay(translator)
	s.logChat(spec.ID, true, http.StatusOK, start, translator, thinking, upstreamURL, err)
}

// thinkingLabel renders the thinking setting actually sent upstream.
func thinkingLabel(req *anthropic.Request) string {
	if req.Thinking == nil {
		return "default"
	}
	if kind, _ := req.Thinking["type"].(string); kind == "disabled" {
		return "disabled"
	}
	if effort, _ := req.OutputConfig["effort"].(string); effort != "" {
		return effort
	}
	return "enabled"
}

func (s *Server) clientFor(cred credential.Credential) *upstream.Client {
	client := *s.client
	if client.BaseURL == "" {
		client.BaseURL = cred.BaseURL
	}
	if client.APIKey == "" {
		client.APIKey = cred.APIKey
	}
	if client.GatewayOrigin == "" {
		client.GatewayOrigin = s.cfg.Upstream.GatewayOrigin
	}
	client.MirrorAuth = s.cfg.Upstream.MirrorAuthEnabled()
	if s.cfg.Upstream.MimicClient {
		client.Headers = mimicHeaders(s.cfg.Upstream.AppVersion, s.cfg.Upstream.ClientTimezone, s.deviceID)
		client.UserAgent = client.Headers["user-agent"]
	}
	return &client
}

// mimicHeaders mirrors the attribution header set the ZCode app itself sends
// on model requests, so the upstream applies the same plan treatment
// (off-peak discounts, free flash windows) as it does for the app.
func mimicHeaders(appVersion, timezone, deviceID string) map[string]string {
	if appVersion == "" {
		appVersion = "3.14.0"
	}
	if timezone == "" {
		timezone = currentTimeZone()
	}
	return map[string]string{
		"http-referer":         "https://zcode.z.ai",
		"user-agent":           "ZCode/" + appVersion,
		"x-zcode-app-version":  appVersion,
		"x-title":              "Z Code@electron", // protocol constant: matches the app byte for byte
		"x-release-channel":    "production",
		"x-client-language":    "en-US",
		"x-client-timezone":    timezone,
		"x-zcode-agent":        "glm",
		"x-platform":           runtime.GOOS + "-" + runtime.GOARCH,
		"x-os-category":        osCategory(),
		"x-os-version":         osVersion(),
		"x-zcode-session-type": "main",
		"x-device-mid":         deviceID,
		"x-request-id":         newUUID(),
		"x-zcode-trace-id":     newUUID(),
		"x-query-id":           newUUID(),
		"x-session-id":         newUUID(),
	}
}

// mimicDeviceID only exposes the installation device id when the gateway is
// impersonating the official client.
func mimicDeviceID(mimic bool, deviceID string) string {
	if !mimic {
		return ""
	}
	return deviceID
}

func currentTimeZone() string {
	// The app reports the IANA zone name; the off-peak window may be resolved
	// against it, so mirror the real zone rather than the Go zone label.
	if data, err := os.ReadFile("/etc/localtime"); err == nil && len(data) > 0 {
		if target, err := os.Readlink("/etc/localtime"); err == nil {
			if idx := strings.Index(target, "zoneinfo/"); idx >= 0 {
				return target[idx+len("zoneinfo/"):]
			}
		}
	}
	if name := os.Getenv("TZ"); name != "" {
		return name
	}
	if name := time.Local.String(); name != "" && !strings.Contains(name, "Local") {
		return name
	}
	return "UTC"
}

func osCategory() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return runtime.GOOS
	}
}

func osVersion() string {
	// The app reports the kernel release (e.g. "27.0.0" on macOS).
	if runtime.GOOS == "darwin" {
		if version, err := syscall.Sysctl("kern.osrelease"); err == nil && version != "" {
			return version
		}
	}
	return "0.0"
}

func newUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	// Format as a UUID so the values look like the app's.
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func (s *Server) storeReplay(t *convert.Translator) {
	calls := t.ToolCalls()
	keys, blocks := t.Replay(calls)
	s.replay.Put(keys, blocks)
}

func (s *Server) logChat(model string, stream bool, status int, start time.Time, t *convert.Translator, thinking, upstreamURL string, err error) {
	usage := t.Usage()
	line := fmt.Sprintf("event=chat model=%s stream=%t status=%d thinking=%s upstream=%s total_ms=%d prompt_tokens=%d completion_tokens=%d finish=%s",
		model, stream, status, thinking, upstreamURL, time.Since(start).Milliseconds(),
		usage.PromptTokens, usage.CompletionTokens, t.FinishReason())
	if err != nil {
		line += fmt.Sprintf(" error=%q", err.Error())
	}
	s.logger.Println(line)
}

func (s *Server) statusFor(err error) int {
	status, _ := s.errorFor(err)
	return status
}

// errorFor maps an upstream failure onto an HTTP status plus OpenAI error body.
func (s *Server) errorFor(err error) (int, openai.ErrorBody) {
	var upstreamErr *upstream.Error
	if errors.As(err, &upstreamErr) {
		status := upstreamErr.Status
		switch {
		case status == http.StatusBadRequest, status == http.StatusUnauthorized,
			status == http.StatusForbidden, status == http.StatusNotFound,
			status == http.StatusRequestEntityTooLarge, status == http.StatusUnprocessableEntity,
			status == http.StatusTooManyRequests:
		case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
			status = http.StatusGatewayTimeout
		default:
			status = http.StatusBadGateway
		}
		kind := upstreamErr.Type
		if kind == "" {
			kind = "upstream_error"
		}
		return status, openai.ErrorBody{Message: upstreamErr.Message, Type: kind, Code: upstreamErr.Code}
	}
	var streamErr *convert.UpstreamError
	if errors.As(err, &streamErr) {
		kind := streamErr.Type
		if kind == "" {
			kind = "upstream_error"
		}
		return http.StatusBadGateway, openai.ErrorBody{Message: streamErr.Message, Type: kind, Code: streamErr.Code}
	}
	if errors.Is(err, context.Canceled) {
		return http.StatusRequestTimeout, openai.ErrorBody{
			Message: "client closed the request before the upstream finished", Type: "client_closed_request",
		}
	}
	return http.StatusBadGateway, openai.ErrorBody{Message: err.Error(), Type: "upstream_error"}
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey == "" {
			next(w, r)
			return
		}
		key := bearerToken(r)
		if key == "" {
			key = r.Header.Get("x-api-key")
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.APIKey)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="glm-zcode-2api"`)
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API key", nil)
			return
		}
		next(w, r)
	}
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	_ = encoder.Encode(payload)
}

func writeError(w http.ResponseWriter, status int, kind, message string, code any) {
	writeJSON(w, status, openai.ErrorResponse{Error: openai.ErrorBody{
		Message: message, Type: kind, Code: code,
	}})
}

// Package upstream talks to the Anthropic-protocol endpoint that Z Code uses.
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"glm-zcode-proxy/internal/anthropic"
)

// Client is a single-upstream Anthropic Messages client.
type Client struct {
	BaseURL       string
	APIKey        string
	APIVersion    string
	UserAgent     string
	Beta          []string
	IdleTimeout   time.Duration
	HeaderTimeout time.Duration
	HTTP          *http.Client
}

// Handlers receives the upstream stream.
type Handlers struct {
	// OnStart runs once the upstream accepted the request, before the body is
	// read. An error aborts the request.
	OnStart func() error
	// OnEvent receives every parsed SSE event.
	OnEvent func(anthropic.Event) error
}

// Error is a failed upstream request.
type Error struct {
	Status     int
	Type       string
	Message    string
	Code       any
	RetryAfter string
}

func (e *Error) Error() string {
	if e.Type == "" {
		return fmt.Sprintf("upstream HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("upstream HTTP %d %s: %s", e.Status, e.Type, e.Message)
}

// Messages posts a streaming Messages request and forwards events.
func (c *Client) Messages(ctx context.Context, req *anthropic.Request, handlers Handlers) error {
	if c.BaseURL == "" {
		return errors.New("upstream base URL is not configured")
	}
	if c.APIKey == "" {
		return errors.New("upstream API key is not available")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode upstream request: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", c.APIKey)
	version := c.APIVersion
	if version == "" {
		version = "2023-06-01"
	}
	httpReq.Header.Set("anthropic-version", version)
	if c.UserAgent != "" {
		httpReq.Header.Set("user-agent", c.UserAgent)
	}
	if len(c.Beta) > 0 {
		httpReq.Header.Set("anthropic-beta", strings.Join(c.Beta, ","))
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return &Error{Status: http.StatusBadGateway, Type: "upstream_unreachable", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readError(resp)
	}

	// Watchdog: the upstream may stream for a long time, but silence for
	// IdleTimeout means the connection is dead.
	watchdog := time.NewTimer(c.idleTimeout())
	defer watchdog.Stop()
	activity := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-reqCtx.Done():
				return
			case <-watchdog.C:
				cancel()
				return
			case <-activity:
				watchdog.Reset(c.idleTimeout())
			}
		}
	}()

	if handlers.OnStart != nil {
		if err := handlers.OnStart(); err != nil {
			return err
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		select {
		case activity <- struct{}{}:
		default:
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			if data.Len() == 0 {
				continue
			}
			event, err := decodeEvent(data.String())
			data.Reset()
			if err != nil {
				return err
			}
			if event == nil {
				continue
			}
			if event.Type == "error" {
				if event.Error != nil {
					return &Error{Status: http.StatusBadGateway, Type: event.Error.Type, Message: event.Error.Message, Code: event.Error.Code}
				}
				return &Error{Status: http.StatusBadGateway, Type: "upstream_error", Message: "unspecified upstream error"}
			}
			if handlers.OnEvent != nil {
				if err := handlers.OnEvent(*event); err != nil {
					return err
				}
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteString("\n")
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:
			// event:, id:, retry:, comments — the JSON carries the type.
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Status: http.StatusBadGateway, Type: "upstream_stream_error", Message: err.Error()}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func (c *Client) idleTimeout() time.Duration {
	if c.IdleTimeout > 0 {
		return c.IdleTimeout
	}
	return 300 * time.Second
}

func decodeEvent(payload string) (*anthropic.Event, error) {
	var event anthropic.Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return nil, fmt.Errorf("decode upstream event: %w", err)
	}
	return &event, nil
}

func readError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	upstreamErr := &Error{
		Status:     resp.StatusCode,
		Type:       http.StatusText(resp.StatusCode),
		Message:    strings.TrimSpace(string(raw)),
		RetryAfter: resp.Header.Get("retry-after"),
	}
	var envelope anthropic.ErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error != nil {
		upstreamErr.Type = envelope.Error.Type
		upstreamErr.Message = envelope.Error.Message
		upstreamErr.Code = envelope.Error.Code
	}
	if upstreamErr.Message == "" {
		upstreamErr.Message = fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
	}
	return upstreamErr
}

// NewHTTPClient builds the transport used for chat streaming. The response
// header timeout bounds the wait for the first byte; the body is unconstrained
// and guarded by the idle watchdog instead.
func NewHTTPClient(headerTimeout time.Duration, maxIdleConns int) *http.Client {
	if maxIdleConns <= 0 {
		maxIdleConns = 8
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConns,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: transport}
}

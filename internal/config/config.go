// Package config loads the gateway configuration: JSON file first, then
// Z2A_* environment overrides (only non-empty variables win).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ModelSpec maps a client-facing model id onto the upstream Anthropic model.
type ModelSpec struct {
	ID              string `json:"id"`
	Upstream        string `json:"upstream"`
	Name            string `json:"name"`
	ContextLength   int    `json:"context_length"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	SupportsImages  bool   `json:"supports_images"`
}

type Server struct {
	MaxBodyMB int `json:"max_body_mb"`
}

type Upstream struct {
	// BaseURL empty means: take it from the ZCode provider entry.
	BaseURL    string `json:"base_url"`
	ProviderID string `json:"provider_id"`
	APIKey     string `json:"api_key"`
	// CredentialConfigPath overrides the ZCode provider configuration path.
	CredentialConfigPath string `json:"credential_config_path"`
	// MimicClient sends the ZCode client's attribution headers so that plan
	// promotions (off-peak discounts, free flash windows) apply to proxied
	// requests the same way they do inside the app.
	MimicClient bool   `json:"mimic_client"`
	AppVersion  string `json:"app_version"`
	// GatewayOrigin is the ZCode platform gateway that official coding-plan
	// model traffic is routed through (plan entitlements are validated there).
	// Empty disables the rewrite and talks to the provider endpoint directly.
	GatewayOrigin string `json:"gateway_origin"`
	// DeviceID is the persistent per-install device id sent as x-device-mid and
	// embedded in metadata.user_id, mirroring the official client.
	DeviceID string `json:"device_id"`
	// ClientTimezone is the IANA zone sent as x-client-timezone. Empty means
	// detect the host zone.
	ClientTimezone       string   `json:"client_timezone"`
	AnthropicVersion     string   `json:"anthropic_version"`
	UserAgent            string   `json:"user_agent"`
	Beta                 []string `json:"beta"`
	TimeoutSeconds       int      `json:"timeout_seconds"`
	HeaderTimeoutSeconds int      `json:"header_timeout_seconds"`
	IdleTimeoutSeconds   int      `json:"idle_timeout_seconds"`
}

type Thinking struct {
	Enabled     bool   `json:"enabled"`
	Effort      string `json:"effort"`
	PromptCache bool   `json:"prompt_cache"`
}

type Config struct {
	Listen   string      `json:"listen"`
	APIKey   string      `json:"api_key"`
	Server   Server      `json:"server"`
	Upstream Upstream    `json:"upstream"`
	Thinking Thinking    `json:"thinking"`
	Models   []ModelSpec `json:"models"`
}

// Default returns the built-in configuration.
// The gateway binds every interface so LAN clients can reach it; the API key
// stays mandatory because the listener carries account credentials.
func Default() *Config {
	return &Config{
		Listen: "0.0.0.0:7864",
		Server: Server{MaxBodyMB: 16},
		Upstream: Upstream{
			GatewayOrigin:        "https://zcode.z.ai",
			ProviderID:           "builtin:bigmodel-coding-plan",
			AnthropicVersion:     "2023-06-01",
			UserAgent:            "glm-zcode-2api/0.1",
			TimeoutSeconds:       120,
			HeaderTimeoutSeconds: 120,
			IdleTimeoutSeconds:   300,
		},
		Thinking: Thinking{Enabled: true, Effort: "max", PromptCache: true},
		Models: []ModelSpec{
			{ID: "glm-5.3", Upstream: "GLM-5.3", Name: "GLM-5.3", ContextLength: 1000000, MaxOutputTokens: 128000},
			{ID: "glm-5.3-flash", Upstream: "GLM-5.3-Flash", Name: "GLM-5.3-Flash", ContextLength: 1000000, MaxOutputTokens: 128000, SupportsImages: true},
		},
	}
}

// Load reads path (optional) on top of Default and applies env overrides.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	cfg.applyEnv()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("Z2A_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("Z2A_API_KEY"); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv("Z2A_UPSTREAM_BASE_URL"); v != "" {
		c.Upstream.BaseURL = v
	}
	if v := os.Getenv("Z2A_UPSTREAM_PROVIDER_ID"); v != "" {
		c.Upstream.ProviderID = v
	}
	if v := os.Getenv("Z2A_UPSTREAM_API_KEY"); v != "" {
		c.Upstream.APIKey = v
	}
	if v := os.Getenv("Z2A_CREDENTIAL_CONFIG_PATH"); v != "" {
		c.Upstream.CredentialConfigPath = v
	}
	if v := os.Getenv("Z2A_CLIENT_TIMEZONE"); v != "" {
		c.Upstream.ClientTimezone = v
	}
	if v := os.Getenv("Z2A_GATEWAY_ORIGIN"); v != "" {
		c.Upstream.GatewayOrigin = v
	}
	if v := os.Getenv("Z2A_DEVICE_ID"); v != "" {
		c.Upstream.DeviceID = v
	}
	if v := os.Getenv("Z2A_USER_AGENT"); v != "" {
		c.Upstream.UserAgent = v
	}
	if v := os.Getenv("Z2A_MAX_BODY_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.MaxBodyMB = n
		}
	}
	if v := os.Getenv("Z2A_THINKING_EFFORT"); v != "" {
		c.Thinking.Effort = v
	}
	if v := os.Getenv("Z2A_THINKING_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Thinking.Enabled = b
		}
	}
	if v := os.Getenv("Z2A_IDLE_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Upstream.IdleTimeoutSeconds = n
		}
	}
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("listen must not be empty")
	}
	if c.Server.MaxBodyMB <= 0 {
		return fmt.Errorf("server.max_body_mb must be positive, got %d", c.Server.MaxBodyMB)
	}
	if len(c.Models) == 0 {
		return fmt.Errorf("models must not be empty")
	}
	seen := make(map[string]bool, len(c.Models))
	for i, m := range c.Models {
		if strings.TrimSpace(m.ID) == "" {
			return fmt.Errorf("models[%d].id must not be empty", i)
		}
		if seen[m.ID] {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		seen[m.ID] = true
		if m.Upstream == "" {
			c.Models[i].Upstream = m.ID
		}
		if m.MaxOutputTokens <= 0 {
			c.Models[i].MaxOutputTokens = 128000
		}
	}
	switch c.Thinking.Effort {
	case "low", "medium", "high", "max":
	default:
		return fmt.Errorf("thinking.effort must be one of low|medium|high|max, got %q", c.Thinking.Effort)
	}
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = 120
	}
	if c.Upstream.HeaderTimeoutSeconds <= 0 {
		c.Upstream.HeaderTimeoutSeconds = c.Upstream.TimeoutSeconds
	}
	if c.Upstream.IdleTimeoutSeconds <= 0 {
		c.Upstream.IdleTimeoutSeconds = 300
	}
	if c.Upstream.AnthropicVersion == "" {
		c.Upstream.AnthropicVersion = "2023-06-01"
	}
	return nil
}

// Model returns the spec for a client-facing model id.
func (c *Config) Model(id string) (ModelSpec, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return ModelSpec{}, false
}

// ModelIDs lists configured client-facing model ids in order.
func (c *Config) ModelIDs() []string {
	ids := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		ids = append(ids, m.ID)
	}
	return ids
}

func (u Upstream) Timeout() time.Duration {
	return time.Duration(u.TimeoutSeconds) * time.Second
}

func (u Upstream) HeaderTimeout() time.Duration {
	return time.Duration(u.HeaderTimeoutSeconds) * time.Second
}

func (u Upstream) IdleTimeout() time.Duration {
	return time.Duration(u.IdleTimeoutSeconds) * time.Second
}

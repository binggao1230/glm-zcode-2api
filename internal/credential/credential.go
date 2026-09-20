// Package credential discovers the upstream API credential from the local
// ZCode installation. Only the API key and base URL are read; the key is
// never written to logs or to the OMP configuration.
package credential

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultConfigPath is the ZCode (desktop app) provider configuration.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".zcode", "v2", "config.json")
}

// Credential is the material needed to talk to the upstream service.
type Credential struct {
	APIKey     string
	BaseURL    string
	ProviderID string
	Provider   string // human readable provider name
	Source     string // file the credential was read from
}

type providerEntry struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Enabled        *bool  `json:"enabled"`
	DisabledReason string `json:"systemDisabledReason"`
	Options        struct {
		APIKey  string `json:"apiKey"`
		BaseURL string `json:"baseURL"`
	} `json:"options"`
}

type zcodeConfig struct {
	Provider map[string]providerEntry `json:"provider"`
}

// Resolver reads and caches the credential, re-reading the ZCode
// configuration when it changes on disk.
type Resolver struct {
	ConfigPath string
	ProviderID string
	// CacheTTL bounds how long a credential is reused when the file is unchanged.
	CacheTTL time.Duration

	mu       sync.Mutex
	modTime  time.Time
	size     int64
	readAt   time.Time
	cached   Credential
	cachedOK bool
}

// Resolve returns the current credential.
func (r *Resolver) Resolve() (Credential, error) {
	path := r.ConfigPath
	if path == "" {
		path = DefaultConfigPath()
	}
	if path == "" {
		return Credential{}, fmt.Errorf("cannot determine the ZCode configuration path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Credential{}, fmt.Errorf("ZCode configuration not found at %s: sign in to ZCode first", path)
	}

	ttl := r.CacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	r.mu.Lock()
	if r.cachedOK && info.ModTime().Equal(r.modTime) && info.Size() == r.size && time.Since(r.readAt) < ttl {
		cred := r.cached
		r.mu.Unlock()
		return cred, nil
	}
	r.mu.Unlock()

	cred, err := load(path, r.ProviderID)
	if err != nil {
		return Credential{}, err
	}

	r.mu.Lock()
	r.cached, r.cachedOK = cred, true
	r.modTime, r.size, r.readAt = info.ModTime(), info.Size(), time.Now()
	r.mu.Unlock()
	return cred, nil
}

func load(path, preferred string) (Credential, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credential{}, fmt.Errorf("read ZCode configuration: %w", err)
	}
	var cfg zcodeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Credential{}, fmt.Errorf("parse ZCode configuration %s: %w", path, err)
	}
	if len(cfg.Provider) == 0 {
		return Credential{}, fmt.Errorf("ZCode configuration %s has no providers", path)
	}

	// An explicitly requested provider must be usable; surface why it is not.
	if preferred != "" {
		entry, ok := cfg.Provider[preferred]
		if !ok {
			return Credential{}, fmt.Errorf("provider %q is not present in %s", preferred, path)
		}
		if entry.Options.APIKey == "" {
			return Credential{}, fmt.Errorf("provider %q has no API key; sign in to ZCode again", preferred)
		}
		if entry.DisabledReason != "" {
			return Credential{}, fmt.Errorf("provider %q is unavailable (%s)", preferred, entry.DisabledReason)
		}
		if entry.Options.BaseURL == "" {
			return Credential{}, fmt.Errorf("provider %q has no base URL", preferred)
		}
		return Credential{
			APIKey:     entry.Options.APIKey,
			BaseURL:    strings.TrimRight(entry.Options.BaseURL, "/"),
			ProviderID: preferred,
			Provider:   entry.Name,
			Source:     path,
		}, nil
	}

	// Otherwise pick the first usable Anthropic-protocol provider.
	ids := make([]string, 0, len(cfg.Provider))
	for id := range cfg.Provider {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry := cfg.Provider[id]
		if entry.Kind != "" && entry.Kind != "anthropic" {
			continue
		}
		if entry.Options.APIKey == "" || entry.Options.BaseURL == "" || entry.DisabledReason != "" {
			continue
		}
		if entry.Enabled != nil && !*entry.Enabled {
			continue
		}
		return Credential{
			APIKey:     entry.Options.APIKey,
			BaseURL:    strings.TrimRight(entry.Options.BaseURL, "/"),
			ProviderID: id,
			Provider:   entry.Name,
			Source:     path,
		}, nil
	}
	return Credential{}, fmt.Errorf("no usable ZCode provider with an API key in %s; sign in to ZCode first", path)
}

package credential

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestResolvePreferredProvider(t *testing.T) {
	path := writeConfig(t, `{"provider":{
		"builtin:bigmodel-coding-plan":{"name":"BigModel - Coding Plan","kind":"anthropic","enabled":true,
			"options":{"apiKey":"key-1","baseURL":"https://open.bigmodel.cn/api/anthropic"}},
		"builtin:zai-coding-plan":{"name":"Z.ai - Coding Plan","kind":"anthropic","enabled":false,
			"systemDisabledReason":"oauth_provider_inactive","options":{"apiKey":"key-2","baseURL":"https://api.z.ai/api/anthropic"}}}}`)

	resolver := &Resolver{ConfigPath: path, ProviderID: "builtin:bigmodel-coding-plan"}
	cred, err := resolver.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.APIKey != "key-1" || cred.BaseURL != "https://open.bigmodel.cn/api/anthropic" {
		t.Fatalf("credential = %+v", cred)
	}
	if cred.Provider != "BigModel - Coding Plan" || cred.Source != path {
		t.Fatalf("credential metadata = %+v", cred)
	}
}

func TestResolveReportsUnusableProvider(t *testing.T) {
	path := writeConfig(t, `{"provider":{
		"builtin:bigmodel-coding-plan":{"name":"BigModel - Coding Plan","kind":"anthropic","enabled":false,
			"systemDisabledReason":"coding_plan_not_entitled","options":{"apiKey":"key-1","baseURL":"https://open.bigmodel.cn/api/anthropic"}}}}`)

	resolver := &Resolver{ConfigPath: path, ProviderID: "builtin:bigmodel-coding-plan"}
	if _, err := resolver.Resolve(); err == nil {
		t.Fatal("a not-entitled provider must be reported, not used")
	}
}

func TestResolvePicksUsableProviderWhenUnspecified(t *testing.T) {
	path := writeConfig(t, `{"provider":{
		"builtin:bigmodel":{"name":"Bigmodel - API Key","kind":"anthropic","options":{"apiKey":"","baseURL":"https://open.bigmodel.cn/api/anthropic"}},
		"builtin:zai-coding-plan":{"name":"Z.ai - Coding Plan","kind":"anthropic","enabled":true,
			"options":{"apiKey":"zai-key","baseURL":"https://api.z.ai/api/anthropic/"}}}}`)

	resolver := &Resolver{ConfigPath: path}
	cred, err := resolver.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.APIKey != "zai-key" || cred.ProviderID != "builtin:zai-coding-plan" {
		t.Fatalf("credential = %+v", cred)
	}
	if cred.BaseURL != "https://api.z.ai/api/anthropic" {
		t.Fatalf("base URL must be normalized: %q", cred.BaseURL)
	}
}

func TestResolveMissingFile(t *testing.T) {
	resolver := &Resolver{ConfigPath: filepath.Join(t.TempDir(), "nope.json")}
	if _, err := resolver.Resolve(); err == nil {
		t.Fatal("missing configuration must fail with an actionable error")
	}
}

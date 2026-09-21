package upstream

import "testing"

func TestMessagesURL(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		gateway string
		want    string
	}{
		{
			name: "bigmodel coding plan goes through the platform gateway",
			base: "https://open.bigmodel.cn/api/anthropic", gateway: "https://zcode.z.ai",
			want: "https://zcode.z.ai/api/v1/ultra/anthropic/v1/messages",
		},
		{
			name: "z.ai coding plan uses the zai gateway path",
			base: "https://api.z.ai/api/anthropic", gateway: "https://zcode.z.ai",
			want: "https://zcode.z.ai/api/v1/ultra-zai/anthropic/v1/messages",
		},
		{
			name: "gateway disabled keeps the provider endpoint",
			base: "https://open.bigmodel.cn/api/anthropic", gateway: "",
			want: "https://open.bigmodel.cn/api/anthropic/v1/messages",
		},
		{
			name: "third-party endpoints are never rewritten",
			base: "https://api.example.com/anthropic", gateway: "https://zcode.z.ai",
			want: "https://api.example.com/anthropic/v1/messages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MessagesURL(tc.base, tc.gateway); got != tc.want {
				t.Fatalf("MessagesURL = %q, want %q", got, tc.want)
			}
		})
	}
}

<div align="center">

# glm-zcode-2api

**A local reverse proxy that turns ZCode (GLM Coding Plan) into an OpenAI-compatible API**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)]()
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](#contributing)

[Project site](https://binggao1230.github.io/glm-zcode-2api/) · [Docs](#-quick-start) · [简体中文](README.md)

</div>

Wraps the **ZCode** (BigModel Coding Plan / Z.ai Coding Plan) Anthropic endpoint into an **OpenAI-compatible API**, so OMP, CLIs and scripts can call GLM-5.3 / GLM-5.3-Flash directly.

- Credentials are **read-only from the local ZCode installation** (`~/.zcode/v2/config.json`) — never copied, the app files are never modified, and no key ever lands in the repo or the OMP config;
- Listens on `0.0.0.0:7864` by default (LAN-accessible) with a mandatory access token, stored at `~/.local/state/glm-zcode-2api/client.key` (0600).

> ⚠️ **Disclaimer**: this is an unofficial gateway that uses *your own* ZCode account as upstream. Personal, local use only. The upstream API and quotas are controlled by Zhipu and may change at any time.

## ✅ Good fit / ❌ Not a fit

**Good fit:**

- You have a GLM Coding Plan subscription and want to call GLM-5.3 / GLM-5.3-Flash from OMP / CLIs / scripts using the **OpenAI protocol**;
- Multiple devices on your LAN share one plan quota (OMP, IDE plugins, scripts);
- You need tool calls, thinking pass-through and streaming, with **off-peak credit discounts applied the same way as inside ZCode**.

**Not a fit:**

- Public services / multi-user resale — there is no multi-tenancy, account pooling or quota governance;
- Bypassing plan limits or billing — upstream errors and billing behavior pass through untouched;
- Anyone needing an admin UI — this is a headless, single-host gateway.

## 🤔 Why not "just set the baseURL"

| Option | Problem |
|---|---|
| Point OMP straight at `open.bigmodel.cn/api/anthropic` | OpenAI clients don't speak the Anthropic protocol; and requests carry no ZCode attribution headers, so off-peak discounts and plan perks are billed as ordinary usage |
| Other zcode2api-style gateways | Each has its own focus; this project adds three things: **attribution header mirroring** (same off-peak treatment), **signed-thinking replay** (required by tool loops, see below), and **zero credential copying** (follows ZCode re-logins automatically) |

## ✨ Features

- **Fully OpenAI-compatible** — `/v1/chat/completions`, `/v1/models`; streaming SSE and aggregated non-streaming modes, drop-in for any OpenAI SDK/CLI
- **Signed thinking replay** — tool loops automatically get the `signature`-bearing thinking block re-injected: OpenAI clients only echo visible text, the gateway remembers signatures in an in-memory LRU
- **Thinking effort pass-through** — `reasoning_effort` → `output_config.effort` (low / medium / high / max), `off` disables explicitly
- **Client attribution (mimic)** — mirrors the ZCode attribution headers and account ID, so off-peak 50% credit discounts and plan perks apply equally
- **Zero credential copying** — reads the ZCode config read-only and re-reads it on change; follows re-logins and plan switches automatically
- **LAN sharing** — `0.0.0.0` listener with a mandatory access token, one-command rotation
- **Honest errors** — upstream 429 / 401 / 400 map to OpenAI errors as-is; mid-stream errors are never disguised as success
- **Single binary** — pure Go standard library, zero third-party dependencies, macOS / Linux / Windows

## 🚀 Quick Start

### Requirements

- ZCode desktop app signed in locally (`~/.zcode/v2/config.json` exists with a usable plan)
- Go ≥ 1.22 (to build); Python 3 only for the launcher

### Build & run

```bash
git clone https://github.com/binggao1230/glm-zcode-2api
cd glm-zcode-2api

CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/glm-zcode-2api ./cmd/server

# Reads the ZCode config, generates the access token, stays in the background
python3 scripts/omp-gateway.py start

curl -s http://127.0.0.1:7864/healthz
# {"service":"glm-zcode-2api","healthy":true,"provider":"builtin:bigmodel-coding-plan","models":2}
```

### Verify

```bash
KEY=$(python3 scripts/omp-gateway.py token)

curl -sN http://127.0.0.1:7864/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}],"stream":true}'
```

### Management commands

```bash
python3 scripts/omp-gateway.py status    # runtime status + health
python3 scripts/omp-gateway.py restart   # after re-logging into ZCode
python3 scripts/omp-gateway.py stop
python3 scripts/omp-gateway.py token     # print the access token (used by OMP)
```

## 🔌 OMP (oh-my-pi) integration

`~/.omp/agent/models.yml`:

```yaml
  zcode:
    baseUrl: http://127.0.0.1:7864/v1
    api: openai-completions
    apiKey: '!/usr/bin/python3 ~/projects/glm-zcode-2api/scripts/omp-gateway.py token'
    authHeader: true
    compat:
      supportsStore: false
      supportsDeveloperRole: false
      maxTokensField: max_tokens
    models:
    - id: glm-5.3
      name: ZCode / GLM-5.3
      reasoning: true
      input: [text]
      contextWindow: 1000000
      maxTokens: 128000
    - id: glm-5.3-flash
      name: ZCode / GLM-5.3-Flash
      reasoning: true
      input: [text, image]
      contextWindow: 1000000
      maxTokens: 128000
```

The credential command **starts the gateway on demand** — no need to keep it running:

```bash
omp --model zcode/glm-5.3-flash
omp models zcode
```

## 🌐 LAN access

The gateway listens on `0.0.0.0:7864` by default:

```bash
KEY=$(python3 ~/projects/glm-zcode-2api/scripts/omp-gateway.py token)   # on the host

curl -s http://192.168.x.x:7864/healthz                                  # from a LAN device (example IP)
curl -s http://192.168.x.x:7864/v1/models -H "Authorization: Bearer $KEY"
```

- Only `/healthz` is unauthenticated (for probes); `/v1/*` and `/status` require the token;
- The token equals usage rights to your plan: if leaked, `rm ~/.local/state/glm-zcode-2api/client.key && python3 scripts/omp-gateway.py restart` rotates it, clients need no changes;
- macOS may prompt to allow incoming connections the first time a LAN device connects.

## 🏷️ Off-peak discounts & request attribution

The new GLM Coding Plan is credit-based: **calls during off-peak hours (including all weekend) consume only 50% of standard credits** ([official docs](https://docs.bigmodel.cn/cn/guide/models/vlm/glm-5.3-flash)). These perks are decided by **client attribution**: the ZCode app attaches a full set of identity headers to model requests; bare third-party requests are billed as ordinary usage.

`upstream.mimic_client` (enabled by the launcher by default) sends the same headers with the app's actual values:

```
user-agent: ZCode/<app version>     http-referer: https://zcode.z.ai
x-zcode-agent: glm                  x-zcode-app-version / x-title / x-release-channel
x-platform: darwin-arm64            x-os-category / x-os-version (kernel release)
x-client-language / x-client-timezone (host IANA zone by default, configurable)
x-request-id / x-zcode-trace-id / x-query-id / x-session-id (generated per request)
metadata.user_id: <account id>
```

`app_version` is read from `ZCode.app/Contents/Info.plist`, `user_id` is decoded from the plan JWT in the ZCode config — nothing to fill in by hand. With mimic off, the gateway calls upstream with only `x-api-key`, identifying as itself.

> Mimicry only makes the request identical to the app's; **whether the perk applies is up to upstream policy**. Confirm it complies with your plan terms.

To verify: run a few `glm-5.3-flash` rounds inside the off-peak window and compare the ZCode usage page / upstream billing against 50% (run the same volume with mimic off as a control).

## ⚙️ Configuration

`config.example.json` is the full reference; the runtime config is written by the launcher to `~/.local/state/glm-zcode-2api/config.json`.

| Field | Default | Notes |
|---|---|---|
| `listen` | `0.0.0.0:7864` | `0.0.0.0` = LAN-accessible (current default), `127.0.0.1:7864` = localhost only |
| `api_key` | generated by launcher | Client access token; empty = no auth |
| `server.max_body_mb` | `16` | Request body limit, over-limit returns 413 |
| `upstream.provider_id` | `builtin:bigmodel-coding-plan` | Which provider entry in the ZCode config to use |
| `upstream.credential_config_path` | `~/.zcode/v2/config.json` | Path to the ZCode config |
| `upstream.base_url` / `api_key` | empty | Overrides the values read from ZCode |
| `upstream.mimic_client` | `true` via launcher | Send ZCode attribution headers (see above); `false` in code |
| `upstream.app_version` | read from the app | `ZCode/<version>` in attribution headers (e.g. `3.14.1`) |
| `upstream.user_id` | decoded from plan JWT | `metadata.user_id` sent with requests |
| `upstream.client_timezone` | empty = detect host | IANA zone for `x-client-timezone`, override with `Z2A_CLIENT_TIMEZONE` |
| `upstream.header_timeout_seconds` | `120` | Wait for upstream response headers |
| `upstream.idle_timeout_seconds` | `300` | Idle limit inside a stream (silent stall) |
| `thinking.enabled` | `true` | Send `thinking.type=enabled` by default |
| `thinking.effort` | `max` | Default effort `low` \| `medium` \| `high` \| `max` (matches ZCode) |
| `thinking.prompt_cache` | `true` | Prompt-cache breakpoints on the system prompt |
| `models[].id` / `.upstream` | — | Client-facing model name / upstream model name |

**The client overrides the config for thinking**: a request carrying `reasoning_effort` (OMP's `--thinking` uses this field) or `reasoning.effort` wins; `minimal`→`low`, `xhigh`→`max`; `off`/`none`/`disabled` or `thinking.type=disabled` turns thinking off. Unset → table defaults.

Environment overrides (only non-empty values apply): `Z2A_LISTEN`, `Z2A_API_KEY`, `Z2A_UPSTREAM_BASE_URL`, `Z2A_UPSTREAM_PROVIDER_ID`, `Z2A_UPSTREAM_API_KEY`, `Z2A_CREDENTIAL_CONFIG_PATH`, `Z2A_CLIENT_TIMEZONE`, `Z2A_USER_AGENT`, `Z2A_MAX_BODY_MB`, `Z2A_THINKING_ENABLED`, `Z2A_THINKING_EFFORT`, `Z2A_IDLE_TIMEOUT_SECONDS`. The launcher filters these out so the environment can't silently change the upstream destination.

## 🚨 Error handling

| Upstream | Gateway | Notes |
|---|---|---|
| 401 / 403 | 401 / 403 | Key invalid: sign into ZCode again, then `restart` |
| 400 `invalid_request_error` | 400 | Passed through verbatim (incl. "model not found" `1211`) |
| 429 `rate_limit_error` `1310` | 429 | Plan quota exhausted; the reset-time message is preserved verbatim |
| ≥500 / network failure | 502 | Upstream unavailable |
| In-stream `error` event | SSE `{"error":…}` + `[DONE]` | An already-open stream is never disguised as a clean success |

## 🩺 Troubleshooting

| Symptom | Cause & fix |
|---|---|
| OMP says `No API key found for zcode` | The token command failed (you are not supposed to configure a key). When the gateway is down the launcher starts it **in parallel and returns the token immediately** (≤2s); if your OMP session predates a rename/move, restart that session |
| `Port 7864 is already occupied` | Another process holds the port: `lsof -nP -iTCP:7864 -sTCP:LISTEN`, or change `listen` |
| Upstream 401, `restart` doesn't help | The ZCode login state changed: sign into the ZCode app again, then `restart` |
| Upstream 400 on the second tool-loop turn | A gateway restart wiped the signed-thinking replay cache — restart that conversation turn |
| LAN device can't connect | Allow it in the macOS firewall; make sure `listen` is `0.0.0.0`, not `127.0.0.1` |

## 📁 Project structure

```
glm-zcode-2api/
├── cmd/server/             # entrypoint: -config <file>
├── internal/config/        # config loading + Z2A_* env overrides
├── internal/credential/    # read-only ZCode config → apiKey/baseURL (cached, re-read on change)
├── internal/openai/        # outbound OpenAI wire types
├── internal/anthropic/     # upstream Anthropic wire types
├── internal/convert/       # request/response/SSE translation + signed-thinking replay
├── internal/upstream/      # upstream HTTP client (SSE parsing, idle watchdog, error classes)
├── internal/server/        # HTTP routes, auth, logging
├── scripts/omp-gateway.py  # OMP launcher: start/stop/status/restart/token
├── docs/                   # project site (GitHub Pages)
└── bin/                    # build output (git-ignored)
```

## 🗺️ Roadmap

- [ ] Automated releases (goreleaser, multi-platform binaries)
- [ ] Docker image (mounted credential directory)
- [ ] End-to-end image-input verification
- [ ] `zcode.z.ai` ZCode-plan endpoint support (requires `Authorization: Bearer` auth)
- [ ] Homebrew tap

## 🤝 Contributing

Issues and PRs welcome:

- Pure Go standard library, single binary — **no third-party dependencies**;
- Run `gofmt -w . && go vet ./... && go test ./...` before submitting;
- **Never commit real credentials** (keys, JWTs, account IDs, client.key).

## 📜 License

[MIT](LICENSE)

## 🙏 Acknowledgements

- [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) — the pioneer of this gateway pattern (CodeBuddy)
- [Z.ai / Zhipu GLM](https://z.ai) — GLM Coding Plan and the GLM-5.3 family
- [can1357/oh-my-pi](https://github.com/can1357/oh-my-pi) — OMP and its custom provider mechanism

---
name: glm-zcode-2api-setup
description: Install and configure glm-zcode-2api — expose a local ZCode (GLM Coding Plan) subscription as an OpenAI-compatible API and wire it into OMP. Use when the user wants to install, start or troubleshoot glm-zcode-2api, or connect their ZCode plan to OpenAI clients / OMP.
---

# glm-zcode-2api setup

glm-zcode-2api is a local reverse proxy that wraps the Anthropic endpoint of a ZCode (GLM Coding Plan / Z.ai Coding Plan) subscription into an OpenAI-compatible API (`/v1/chat/completions`, `/v1/models`).

The full procedure, acceptance criteria and safety rules live in `AI_SETUP.md` at the repository root: <https://github.com/binggao1230/glm-zcode-2api/blob/main/AI_SETUP.md>

## Summary

1. Prerequisites: ZCode signed in locally (`~/.zcode/v2/config.json`), Go ≥ 1.22, Python 3.
2. Build: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/glm-zcode-2api ./cmd/server`
3. Start: `python3 scripts/omp-gateway.py start` → health check `curl -s http://127.0.0.1:7864/healthz`.
4. Token: `python3 scripts/omp-gateway.py token` (secret — never expose).
5. OMP: merge the `zcode` provider into `~/.omp/agent/models.yml`, with `apiKey` pointing at the token-printing command; generate the `models` list from `/v1/models` and the runtime config instead of copying examples (see step 7 of `AI_SETUP.md`).
6. Acceptance: `omp models zcode` lists the models; `omp --model zcode/glm-5.3-flash` answers normally.

## Discipline

- Never commit or print real credentials (client.key, upstream apiKey, account IDs);
- Token auth is mandatory — do not disable it; never modify ZCode app files;
- Troubleshooting log: `~/.local/state/glm-zcode-2api/gateway.log`.

# AI_SETUP.md — glm-zcode-2api automated install & configuration

> This document is written for AI coding agents (OMP / Claude Code / Cursor etc.). Humans: read the [README](README.md).

Set up glm-zcode-2api on this machine: install, start the gateway and wire it into OMP. Follow the steps exactly and report the result of every step as you go.

## Background

glm-zcode-2api is a local reverse proxy that wraps the Anthropic endpoint of a ZCode (GLM Coding Plan / Z.ai Coding Plan) subscription into an OpenAI-compatible API. Repository: <https://github.com/binggao1230/glm-zcode-2api>

## Steps

1. **Environment check**: `~/.zcode/v2/config.json` must exist (ZCode is signed in); Go ≥ 1.22 and Python 3 must be installed (`go version`, `python3 --version`).
2. **Get the code**: `git clone https://github.com/binggao1230/glm-zcode-2api` and cd into it (`<repo>` below = that path).
3. **Build**: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/glm-zcode-2api ./cmd/server`
4. **Start**: `python3 scripts/omp-gateway.py start`
5. **Verify the gateway**: `curl -s http://127.0.0.1:7864/healthz` should return `{"service":"glm-zcode-2api","healthy":true,...}`. If unhealthy, read `~/.local/state/glm-zcode-2api/gateway.log` and fix before retrying.
6. **Get the access token**: `python3 scripts/omp-gateway.py token` (that output is the token; never write it to any file or show it publicly).
7. **Wire up OMP (oh-my-pi)**: first discover the plan's actual models, then generate the config —
   - `KEY=$(python3 scripts/omp-gateway.py token)`
   - `curl -s http://127.0.0.1:7864/v1/models -H "Authorization: Bearer $KEY"` returns the plan's model id list
   - read the `models` array in `~/.local/state/glm-zcode-2api/config.json` for each model's `context_length`, `max_output_tokens` and `supports_images`
   - then edit `~/.omp/agent/models.yml` and merge the following skeleton under providers (update the `zcode` entry if it exists; replace `<repo>` with the actual path; generate `models` from the results above, `name` = `ZCode / <model>`, use `input: [text, image]` when `supports_images: true` otherwise `[text]`):

   ```yaml
     zcode:
       baseUrl: http://127.0.0.1:7864/v1
       api: openai-completions
       apiKey: '!/usr/bin/python3 <repo>/scripts/omp-gateway.py token'
       authHeader: true
       compat:
         supportsStore: false
         supportsDeveloperRole: false
         maxTokensField: max_tokens
       models:
       # generate this list from /v1/models and the runtime config — do not copy blindly
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

8. **Acceptance**:
   - `omp models zcode` lists the models;
   - `omp --model zcode/glm-5.3-flash` answers with a normal stream;
   - if any step fails: read `~/.local/state/glm-zcode-2api/gateway.log` and the command stderr, fix and retry — never skip acceptance.

## Verifying byte-level fidelity (optional)

The launcher can capture what the official client actually sends, by pointing the
client at a local listener via its endpoint override:

```bash
python3 scripts/omp-gateway.py capture          # listens on 127.0.0.1:7865
# in another terminal, launch the client against it:
ZCODE_ENDPOINT_ORIGIN=http://127.0.0.1:7865 open -a ZCode --env ZCODE_ENDPOINT_ORIGIN=http://127.0.0.1:7865
```

Send one message in the client; every request (path, headers, body hash and
preview) is appended to `~/.local/state/glm-zcode-2api/capture.jsonl` (0600).
Compare it against what this gateway sends to confirm the two are identical.
The capture file contains live credentials — treat it as secret and delete it
when done.

## Discipline

- Never commit or publicly print `client.key`, the upstream apiKey, JWTs or account IDs;
- The gateway listens on `0.0.0.0:7864` with mandatory token auth — do not disable it;
- Do not modify any ZCode app files; the gateway only reads its config;
- Token rotation: delete `~/.local/state/glm-zcode-2api/client.key`, then `restart`.

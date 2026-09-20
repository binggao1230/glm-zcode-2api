---
name: glm-zcode-2api-setup
description: 安装并配置 glm-zcode-2api——把本机 ZCode（GLM Coding Plan）订阅变成 OpenAI 兼容 API，并接入 OMP。当用户想要安装、启动、排查 glm-zcode-2api，或把 ZCode 订阅接入 OpenAI 客户端 / OMP 时使用。
---

# glm-zcode-2api 安装与配置

glm-zcode-2api 是一个本机反向代理：把 ZCode（GLM Coding Plan / Z.ai Coding Plan）订阅的 Anthropic 端点包装成 OpenAI 兼容 API（`/v1/chat/completions`、`/v1/models`）。

完整步骤、验收标准与安全纪律见仓库根目录的 `AI_SETUP.md`；仓库地址：<https://github.com/binggao1230/glm-zcode-2api>

## 摘要

1. 前置：ZCode 已登录（`~/.zcode/v2/config.json`）、Go ≥ 1.22、Python 3。
2. 构建：`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/glm-zcode-2api ./cmd/server`
3. 启动：`python3 scripts/omp-gateway.py start` → 健康检查 `curl -s http://127.0.0.1:7864/healthz`。
4. 口令：`python3 scripts/omp-gateway.py token`（机密，勿外泄）。
5. OMP：把 `zcode` provider 合并进 `~/.omp/agent/models.yml`，`apiKey` 指向取口令命令；`models` 列表按 `/v1/models` 与运行配置生成，不要照抄示例（见 `AI_SETUP.md` 第 7 步）。
6. 验收：`omp models zcode` 列出模型；`omp --model zcode/glm-5.3-flash` 正常回答。

## 纪律

- 不提交、不打印任何真实凭据（client.key、上游 apiKey、账号 ID）；
- 口令校验强制开启，不得关闭；不修改 ZCode App 文件；
- 排查日志：`~/.local/state/glm-zcode-2api/gateway.log`。

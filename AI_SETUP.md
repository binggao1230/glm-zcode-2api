# AI_SETUP.md — glm-zcode-2api 自动安装与配置

> 本文档面向 AI 编码代理（OMP / Claude Code / Cursor 等）。人类请阅读 [README](README.md)。
> This file is written for AI coding agents. Humans: read the README.

请在本机完成 glm-zcode-2api 的安装、启动与 OMP 接入。严格按以下步骤执行，完成后逐步报告每一步的结果。

## 背景

glm-zcode-2api 是一个本机反向代理：把 ZCode（GLM Coding Plan / Z.ai Coding Plan）订阅的 Anthropic 端点包装成 OpenAI 兼容 API。仓库：<https://github.com/binggao1230/glm-zcode-2api>

## 步骤

1. **环境检查**：确认 `~/.zcode/v2/config.json` 存在（说明 ZCode 已登录）；确认已安装 Go ≥ 1.22 与 Python 3（`go version`、`python3 --version`）。
2. **获取代码**：`git clone https://github.com/binggao1230/glm-zcode-2api` 并进入该目录（下文 `<repo>` 以实际克隆路径为准）。
3. **构建**：`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/glm-zcode-2api ./cmd/server`
4. **启动**：`python3 scripts/omp-gateway.py start`
5. **验证网关**：`curl -s http://127.0.0.1:7864/healthz` 应返回 `{"service":"glm-zcode-2api","healthy":true,...}`；不健康时读取 `~/.local/state/glm-zcode-2api/gateway.log` 排查。
6. **取访问口令**：`python3 scripts/omp-gateway.py token`（输出即口令；不要把它写进任何文件或公开展示）。
7. **接入 OMP（oh-my-pi）**：编辑 `~/.omp/agent/models.yml`，在 providers 下合并以下内容（已有 `zcode` 就更新；`<repo>` 替换为实际路径）：

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
       # 模型以套餐实际提供为准，按需增删
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

8. **验收**：
   - `omp models zcode` 能列出模型；
   - `omp --model zcode/glm-5.3-flash` 能正常流式回答；
   - 每一步失败时：读取 `~/.local/state/glm-zcode-2api/gateway.log` 与命令 stderr，修正后重试，不要跳过验收。

## 纪律

- 任何时候不得把 `client.key`、上游 apiKey、JWT 或账号 ID 提交进仓库或打印到公开场合；
- 网关默认监听 `0.0.0.0:7864` 且口令校验强制开启，**不得关闭**；
- 不修改 ZCode App 的任何文件；本网关只读取其配置；
- 口令轮换：删除 `~/.local/state/glm-zcode-2api/client.key` 后 `restart`。

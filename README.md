<div align="center">

# glm-zcode-2api

**把 ZCode（GLM Coding Plan）变成 OpenAI 兼容 API 的本机反向代理**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-blue)]()
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](#contributing)

[项目主页](https://binggao1230.github.io/glm-zcode-2api/) · [文档](#-快速开始) · [English](README.en.md)

</div>

把本机 **ZCode**（BigModel Coding Plan / Z.ai Coding Plan）的 Anthropic 端点包装成 **OpenAI 兼容 API**，供 OMP、各类 CLI 与脚本直接调用。

- 凭据**只读 ZCode 本机配置**（`~/.zcode/v2/config.json`），不复制、不改写 App 文件，密钥不落仓库、不进 OMP 配置；
- 默认监听 `0.0.0.0:7864`（局域网可访问），访问口令强制；口令由启动器生成，仅存本机 `~/.local/state/glm-zcode-2api/client.key`（0600）。

> ⚠️ **合规须知**：本项目是非官方网关，使用你本人的 ZCode 账号作为上游，仅限本人账号、本机 / 私有环境自用。上游接口与配额由智谱侧控制，可能随时变化。

## ✅ 适合 / ❌ 不适合

**适合：**

- 你有 GLM Coding Plan 订阅，想在 OMP / CLI / 脚本里用 **OpenAI 协议**调用 GLM-5.3 / GLM-5.3-Flash；
- 局域网内多设备（OMP、IDE 插件、脚本）共享同一份套餐额度；
- 需要工具调用、思考透传、流式输出，且**闲时积分优惠与 ZCode 内使用同权**。

**不适合：**

- 公开服务 / 多用户转售——没有多租户、账号池与配额治理；
- 绕过套餐限制或计费——上游错误与计费行为原样透传，不做任何伪装；
- 需要后台管理界面——这是无 UI 的单机网关。

## 🤔 为什么不是"直接填 baseURL"

| 方案 | 问题 |
|---|---|
| OMP 直连 `open.bigmodel.cn/api/anthropic` | OpenAI 协议客户端说不了 Anthropic 协议；且请求无 ZCode 归因头，闲时优惠与套餐权益按普通调用记账 |
| 其他 zcode2api 类网关 | 各有侧重；本项目额外做了三件事：**归因头镜像**（闲时优惠同权）、**签名思考回放**（工具循环必需，见下）、**凭据零搬运**（ZCode 重新登录后自动跟随，无需改任何配置） |

## ✨ 特性

- **OpenAI 完全兼容** — `/v1/chat/completions`、`/v1/models`；流式 SSE 与非流式聚合双模式，任意 OpenAI SDK / CLI 零改造接入
- **签名思考回放** — 工具循环自动补回带 `signature` 的 thinking 块：OpenAI 客户端只回显可见文本，网关在内存 LRU 中记住签名并自动补回
- **思考档位透传** — `reasoning_effort` → `output_config.effort`（low / medium / high / max），`off` 显式关闭
- **客户端归因（mimic）** — 完整镜像 ZCode 归因头与账号 ID，闲时 50% 积分优惠与套餐权益同等生效
- **凭据零搬运** — 只读 ZCode 配置，App 重新登录 / 切换套餐后自动跟随（按文件变更重读）
- **局域网共享** — `0.0.0.0` 监听 + 访问口令强制，口令一键轮换
- **错误如实透传** — 上游 429 / 401 / 400 原样映射为 OpenAI 错误，流内错误不会被伪装成正常结束
- **单二进制** — 纯 Go 标准库、零第三方依赖，macOS / Linux / Windows

## 🚀 快速开始

### 环境要求

- 本机已登录 ZCode 桌面版（`~/.zcode/v2/config.json` 存在且含可用套餐）
- Go ≥ 1.22（源码构建）；Python 3 仅启动器需要

### 构建并启动

```bash
git clone https://github.com/binggao1230/glm-zcode-2api
cd glm-zcode-2api

CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/glm-zcode-2api ./cmd/server

# 读取 ZCode 配置、生成访问口令、后台常驻
python3 scripts/omp-gateway.py start

curl -s http://127.0.0.1:7864/healthz
# {"service":"glm-zcode-2api","healthy":true,"provider":"builtin:bigmodel-coding-plan","models":2}
```

### 验证

```bash
KEY=$(python3 scripts/omp-gateway.py token)

curl -sN http://127.0.0.1:7864/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}],"stream":true}'
```

### 管理命令

```bash
python3 scripts/omp-gateway.py status    # 运行状态 + health
python3 scripts/omp-gateway.py restart   # 重新登录 ZCode / 切换套餐后同步
python3 scripts/omp-gateway.py stop
python3 scripts/omp-gateway.py token     # 打印访问口令（OMP 按需启动时用）
```

## 🔌 接入 OMP（oh-my-pi）

`~/.omp/agent/models.yml`：

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

凭据解析命令会**按需拉起网关**（未运行则自动 `start`），无需手工常驻：

```bash
omp --model zcode/glm-5.3-flash
omp models zcode
```

## 🌐 局域网访问

网关默认监听 `0.0.0.0:7864`，同网段设备直接可用：

```bash
KEY=$(python3 ~/projects/glm-zcode-2api/scripts/omp-gateway.py token)   # 本机取口令

curl -s http://192.168.x.x:7864/healthz                                  # 局域网设备上（示例 IP）
curl -s http://192.168.x.x:7864/v1/models -H "Authorization: Bearer $KEY"
```

- 仅 `/healthz` 不鉴权（探活用）；`/v1/*` 与 `/status` 全部要求口令；
- 口令即账号额度使用权：泄漏后 `rm ~/.local/state/glm-zcode-2api/client.key && python3 scripts/omp-gateway.py restart` 自动轮换，客户端无需改配置；
- 首次从其他设备连接时，macOS 防火墙可能弹出「允许传入连接」，放行即可。

## 🏷️ 闲时优惠与请求归因

新版 GLM Coding Plan 按积分计费，**非高峰时段（含周末全天）的调用只消耗 50% 标准积分**（[官方说明](https://docs.bigmodel.cn/cn/guide/models/vlm/glm-5.3-flash)）。这类权益按**客户端归因**判定：ZCode App 在模型请求上携带一整套标识头，裸的第三方请求没有这套身份，服务端按普通调用记账。

网关的 `upstream.mimic_client`（启动器默认开启）按 App 的实际取值原样发出：

```
user-agent: ZCode/<App 版本>        http-referer: https://zcode.z.ai
x-zcode-agent: glm                  x-zcode-app-version / x-title / x-release-channel
x-platform: darwin-arm64            x-os-category / x-os-version（内核版本）
x-client-language / x-client-timezone（默认探测本机 IANA 时区，可用 upstream.client_timezone 指定）
x-request-id / x-zcode-trace-id / x-query-id / x-session-id（每请求生成）
metadata.user_id: <账号 ID>
```

`app_version` 从 `ZCode.app/Contents/Info.plist` 读取，`user_id` 从 ZCode 配置里的套餐 JWT 解出，无需手工填写。关闭 mimic 后网关只发 `x-api-key`，以自己的身份调用上游。

> mimic 只是让请求与 App 完全一致，**是否享受优惠由上游策略决定**；使用前请自行确认符合你的套餐条款。

验证方法：闲时窗口内用 `glm-5.3-flash` 跑几轮，对比 ZCode 用量页 / 上游账单是否按 50% 计（关闭 mimic 跑同样的量作对照）。

## ⚙️ 配置说明

`config.example.json` 是完整参考；实际运行配置由启动器写入 `~/.local/state/glm-zcode-2api/config.json`。

| 字段 | 默认 | 说明 |
|---|---|---|
| `listen` | `0.0.0.0:7864` | 监听地址：`0.0.0.0` = 局域网可访问（当前默认），`127.0.0.1:7864` = 仅本机 |
| `api_key` | 由启动器生成 | 客户端访问口令；空 = 不鉴权 |
| `server.max_body_mb` | `16` | 请求体上限，超限返回 413 |
| `upstream.provider_id` | `builtin:bigmodel-coding-plan` | 取 ZCode 配置里哪个 provider 的密钥 |
| `upstream.credential_config_path` | `~/.zcode/v2/config.json` | ZCode 配置路径 |
| `upstream.base_url` / `api_key` | 空 | 非空则覆盖从 ZCode 读到的值 |
| `upstream.mimic_client` | 启动器写 `true` | 以 ZCode 客户端身份发送归因头（见上节）；代码默认 `false` |
| `upstream.app_version` | 读取 App 实际版本 | 归因头里的 `ZCode/<version>`（如 `3.14.1`） |
| `upstream.user_id` | 从套餐 JWT 提取 | 随请求发送的 `metadata.user_id`（账号 ID） |
| `upstream.client_timezone` | 空 = 按本机探测 | 归因头 `x-client-timezone` 用的 IANA 时区，可用 `Z2A_CLIENT_TIMEZONE` 覆盖 |
| `upstream.header_timeout_seconds` | `120` | 等上游响应头上限 |
| `upstream.idle_timeout_seconds` | `300` | 流中空闲上限（静默断流） |
| `thinking.enabled` | `true` | 默认是否发送 `thinking.type=enabled` |
| `thinking.effort` | `max` | 默认档位 `low` \| `medium` \| `high` \| `max`（对齐 ZCode 默认档） |
| `thinking.prompt_cache` | `true` | 给 system 打 prompt cache 断点 |
| `models[].id` / `.upstream` | — | 客户端模型名 / 上游模型名 |

**思考档位由客户端覆盖配置**：请求带 `reasoning_effort`（OMP 的 `--thinking` 即走此字段）或 `reasoning.effort` 时以客户端为准；`minimal`→`low`、`xhigh`→`max`，`off`/`none`/`disabled` 或 `thinking.type=disabled` 则关闭。未指定时用上表默认值。

环境变量覆盖（非空才生效）：`Z2A_LISTEN`、`Z2A_API_KEY`、`Z2A_UPSTREAM_BASE_URL`、`Z2A_UPSTREAM_PROVIDER_ID`、`Z2A_UPSTREAM_API_KEY`、`Z2A_CREDENTIAL_CONFIG_PATH`、`Z2A_CLIENT_TIMEZONE`、`Z2A_USER_AGENT`、`Z2A_MAX_BODY_MB`、`Z2A_THINKING_ENABLED`、`Z2A_THINKING_EFFORT`、`Z2A_IDLE_TIMEOUT_SECONDS`。启动器会过滤掉这些变量，避免环境意外改变上游目的地。

## 🚨 错误处理

| 上游 | 网关 | 说明 |
|---|---|---|
| 401 / 403 | 401 / 403 | 密钥失效：在 ZCode 重新登录后 `restart` |
| 400 `invalid_request_error` | 400 | 原样透传（含「模型不存在」`1211` 等） |
| 429 `rate_limit_error` `1310` | 429 | 计划配额耗尽，原样保留重置时间文案 |
| ≥500 / 网络失败 | 502 | 上游不可用 |
| 流内 `error` 事件 | SSE `{"error":…}` + `[DONE]` | 已开始的流不会被伪装成正常结束 |

## 🩺 常见问题（Troubleshooting）

| 症状 | 原因与处置 |
|---|---|
| OMP 报 `No API key found for zcode` | 取口令命令失败了（不是要你配 key）。网关停着时启动器会**并行拉起并立即返回口令**（≤2s）；若 OMP 会话是在改目录/改名之前开的，重启该会话即可 |
| `Port 7864 is already occupied` | 端口被别的进程占了：`lsof -nP -iTCP:7864 -sTCP:LISTEN` 找到后处理，或改 `listen` |
| 上游 401，且 `restart` 无效 | ZCode 登录态变了：打开 ZCode App 重新登录，再 `restart` |
| 第二轮工具调用上游 400 | 网关重启清空了签名思考回放缓存——重新开始该轮对话即可 |
| 局域网设备连不上 | macOS 防火墙放行；确认 `listen` 是 `0.0.0.0` 而非 `127.0.0.1` |

## 📁 项目结构

```
glm-zcode-2api/
├── cmd/server/             # 入口：-config 指定配置
├── internal/config/        # 配置加载 + Z2A_* 环境覆盖
├── internal/credential/    # 只读 ZCode 配置，取 apiKey/baseURL（缓存 + 变更重读）
├── internal/openai/        # 对外 OpenAI 线格式
├── internal/anthropic/     # 上游 Anthropic 线格式
├── internal/convert/       # 请求/响应/SSE 转换 + 签名思考回放
├── internal/upstream/      # 上游 HTTP 客户端（SSE 解析、空闲看门狗、错误归类）
├── internal/server/        # HTTP 路由、鉴权、日志
├── scripts/omp-gateway.py  # OMP 启动器：start/stop/status/restart/token
├── docs/                   # 项目主页（GitHub Pages）
└── bin/                    # 构建产物（git 忽略）
```

## 🗺️ Roadmap

- [ ] Release 自动化（goreleaser 多平台二进制）
- [ ] Docker 镜像（凭据目录挂载）
- [ ] 图片输入端到端验证
- [ ] `zcode.z.ai` ZCode 自有套餐端点支持（需 `Authorization: Bearer` 鉴权方式）
- [ ] Homebrew tap

## 🤝 Contributing

欢迎 Issue 与 PR：

- 纯 Go 标准库、单二进制——**不引入第三方依赖**；
- 提交前跑 `gofmt -w . && go vet ./... && go test ./...`；
- **严禁提交任何真实凭据**（key、JWT、账号 ID、client.key）。

## 📜 License

[MIT](LICENSE)

## 🙏 致谢

- [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) —— 同类思路（CodeBuddy 网关）的先行者
- [Z.ai / 智谱 GLM](https://z.ai) —— GLM Coding Plan 与 GLM-5.3 系列
- [can1357/oh-my-pi](https://github.com/can1357/oh-my-pi) —— OMP 及其自定义 provider 机制

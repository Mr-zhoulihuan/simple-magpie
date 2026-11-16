# 总体架构

## 1. 分层视图

```mermaid
flowchart TB
  subgraph Clients["客户端 Agents"]
    CC["Claude Code<br/>(Anthropic)"]
    CX["Codex<br/>(Responses)"]
    GC["Gemini CLI<br/>(Gemini)"]
    PI["pi / OpenCode<br/>(Chat)"]
  end

  subgraph SM["simple-magpie 进程 (单二进制, Go)"]
    direction TB
    MUX["HTTP mux<br/>cmd/simple-magpie"]
    subgraph GW["internal/gateway 网关"]
      HANDLE["handle()<br/>解析模型/会话"]
      PLAN["routing.Plan()<br/>候选链"]
      TRY["tryOne()<br/>relay 或 translate"]
      RELAY["relay / relaySSE<br/>同协议转发"]
      TRANS["translateReply<br/>跨协议转换"]
    end
    subgraph PROTO["internal/protocol 协议层"]
      IR["统一 IR<br/>Request / Reply"]
    end
    subgraph ANALYSIS["internal/prompt + transcript"]
      PROMPT["prompt.Analyze<br/>prompt 解剖"]
      TRANSCRIBE["transcript<br/>对话转写"]
    end
    STORE["internal/store<br/>内存记录 / 会话"]
    WEBSRV["internal/web<br/>仪表盘 + JSON API"]
  end

  subgraph Upstream["上游 Providers"]
    DS["DeepSeek (chat)"]
    AN["Anthropic (anthropic)"]
    OA["OpenAI (chat/responses)"]
    MG["另一个 magpie"]
  end

  CC --> MUX
  CX --> MUX
  GC --> MUX
  PI --> MUX
  MUX --> HANDLE
  HANDLE --> PROMPT
  HANDLE --> TRANSCRIBE
  HANDLE --> PLAN
  PLAN --> TRY
  TRY --> RELAY
  TRY --> TRANS
  RELAY --> IR
  TRANS --> IR
  TRY --> DS
  TRY --> AN
  TRY --> OA
  TRY --> MG
  HANDLE --> STORE
  STORE --> WEBSRV
  WEBSRV -. 浏览器访问 .-> Browser["浏览器 /dashboard"]
```

## 2. 包职责

| 包 | 路径 | 职责 |
| --- | --- | --- |
| `main` | `cmd/simple-magpie` | 入口:加载配置,把网关与仪表盘挂到同一个端口 |
| `config` | `internal/config` | 读取 `providers / accounts / groups` JSON,校验 |
| `protocol` | `internal/protocol` | 统一 IR + 四协议 `Parse`/`Build`/`Detect`/`UpstreamPath` |
| `gateway` | `internal/gateway` | HTTP 处理、候选循环、relay/translate、记录 |
| `routing` | `internal/routing` | 候选规划、策略、账号冷却、会话亲缘 |
| `prompt` | `internal/prompt` | prompt 解剖、token 估算、缓存命中、headroom |
| `transcript` | `internal/transcript` | IR ⇄ 可展示的对话 turns |
| `store` | `internal/store` | 内存保存请求记录与会话序列、按轮次聚合 |
| `web` | `internal/web` | 内嵌仪表盘页面 + `/dashboard/*` JSON API |

## 3. 依赖方向(单向,无环)

```mermaid
flowchart LR
  main --> gateway
  main --> web
  gateway --> routing
  gateway --> protocol
  gateway --> prompt
  gateway --> transcript
  gateway --> store
  web --> store
  store --> prompt
  store --> transcript
  prompt --> protocol
  transcript --> protocol
  routing --> config
```

- 核心 `protocol` 只依赖标准库(以及 `prompt`/`transcript` 反向使用它的 IR,但 `protocol` 不依赖它们)。
- `gateway` 是编排层,汇集所有能力。
- `web` 只读 `store`,与网关解耦(共用同一进程)。

## 4. 运行时同时服务两条路径

```mermaid
flowchart LR
  Port["127.0.0.1:PORT"] --> M{"路径?"}
  M -->|"/v1/* /messages /responses /v1beta/*"| GW["网关 (gateway.Handler)"]
  M -->|"/dashboard, /dashboard/*"| WEB["仪表盘 (web.Handler)"]
  M -->|"GET /"| INFO["index: 端点说明"]
```

网关自身处理的端点:

| 端点 | 协议 |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat |
| `POST /v1/responses` | OpenAI Responses |
| `POST /v1/messages` | Anthropic Messages |
| `POST /v1beta/models/{model}:generateContent` | Gemini |
| `GET /v1/models` | 模型列表 |

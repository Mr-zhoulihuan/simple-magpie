# 一次请求的完整流程

## 1. 总时序

```mermaid
sequenceDiagram
  autonumber
  participant A as Agent
  participant G as gateway.handle
  participant P as prompt/transcript
  participant R as routing.Plan
  participant T as tryOne
  participant U as 上游 Provider
  participant S as store
  participant W as web dashboard

  A->>G: POST /v1/... (body)
  G->>G: protocol.Detect(path) 得到 from
  G->>P: prompt.Analyze + transcript.FromRequest
  G->>R: Plan(model, session)
  R-->>G: 候选链(按策略+亲和排序)
  loop 每个候选直到成功或不重试
    G->>T: tryOne(from, provider, candidate, body, rec)
    alt provider 支持 from 协议
      T->>U: relay 原样转发
      U-->>T: SSE 或 JSON
      T->>G: 边收边写回, 读 usage
    else 不支持
      T->>T: Parse(from) 得 IR, Build(to) 得上游 body
      T->>U: 发送
      U-->>T: SSE 或 JSON
      T->>T: gatherReply(to) 得 IR Reply, renderReply(from) 写回
    end
    Note over T: 4xx/5xx 可重试则 Rest(账号) 并尝试下一个
  end
  G->>S: calibrate(prompt,usage) + AppendReply + Add(record)
  W->>S: /dashboard/* 读记录 (浏览器轮询)
```


## 2. `handle()` 内部步骤

```mermaid
flowchart TB
  Start(["读到 body"]) --> Det["from = protocol.Detect(path)"]
  Det --> GModel["Gemini? 从 path 取 model 塞进 body"]
  GModel --> Model["model = modelOf(body)<br/>session = sessionOf(r)"]
  Model --> Analyze["rec.Prompt = prompt.Analyze(from, body)<br/>rec.Prompt.Window = ContextWindow(model)"]
  Analyze --> Tr["req = Parse(from, body)<br/>full = transcript.FromRequest(req)<br/>rec.AllCount = len(full)<br/>rec.Turns = full[prior:]  ← 只留本轮新增"]
  Tr --> Plan["cands, resolved = routing.Plan(model, session)"]
  Plan --> Empty{"候选为空?"}
  Empty -->|是| R404["状态 404 / 502 → 写回 → 存记录"]
  Empty -->|否| Loop["遍历 candidates"]
  Loop --> Try["status, served, retryable = tryOne(...)"]
  Try --> Served{"served?"}
  Served -->|是| OK["calibrate 到 usage 真实总量<br/>AppendReply(助手回复)<br/>CacheHit<br/>router.Served(session,...)<br/>store.Add(rec)"]
  Served -->|否| Trail["rec.Tried += 本次尝试<br/>retryable? 继续/休息账号<br/>否则 break"]
  Trail --> Loop
```

## 3. `tryOne()` 内部步骤

```mermaid
flowchart TB
  In(["tryOne"]) --> To["to = from 若 provider 支持,否则 firstProtocol(p)"]
  To --> Relay{"p.Speaks(from)?"}
  Relay -->|是 relay| SB["sendBody = body(原样)"]
  Relay -->|否 translate| PB["req=Parse(from); sendBody=Build(to, req, c.Model)"]
  SB --> RM["rewriteModel(sendBody, c.Model)"]
  PB --> RM
  RM --> URL["url = UpstreamPath(base, to, model, stream)"]
  URL --> HDR["copyHeaders: 注入 Authorization/x-api-key/x-goog-api-key<br/>保留 session_id"]
  HDR --> Do["client.Do(req)"]
  Do --> Err{"网络错误?"}
  Err -->|是| R502["502, retryable=true"]
  Err -->|否| Code{"status >= 400?"}
  Code -->|是| Retry{"429/402/5xx?"}
  Retry -->|可重试| RR["记错, retryable=true(不写回)"]
  Retry -->|否| Pass["原样写回客户端, retryable=false"]
  Code -->|否| Same{"relay?"}
  Same -->|是| RL["relay(res): 复制头, SSE 边读边 flush, 读 usage"]
  Same -->|否| TL["translateReply: gatherReply(to) → renderReply(from)"]
```

## 4. relay 与 translate 的返回处理

```mermaid
flowchart LR
  subgraph relayPath["relay(同协议)"]
    R1["复制响应头(去掉 hop-by-hop)"]
    R2{"text/event-stream?"}
    R3["relaySSE: 逐行转发 + collectUsage 累计 usage/回复"]
    R4["读整个 JSON body → usageOf + parseReplyJSON 取回复"]
    R1 --> R2
    R2 -->|是| R3
    R2 -->|否| R4
  end
  subgraph transPath["translate(跨协议)"]
    T1["gatherReply(to): 聚合上游回复<br/>(流式则逐事件合并 tool_call/usage)"]
    T2["组装 IR Reply{Text, Calls, Usage}"]
    T3{"客户端要流式?"}
    T4["writeTranslatedStream(from): 生成目标协议 SSE"]
    T5["renderReply(from): 生成目标协议 JSON"]
    T1 --> T2 --> T3
    T3 -->|是| T4
    T3 -->|否| T5
  end
```

## 5. 关键设计取舍

| 点 | 选择 | 原因 |
| --- | --- | --- |
| relay vs translate | 能原生就 relay | 保留厂商私有字段(缓存断点、原生搜索、sealed 内容) |
| 翻译的流式 | 先聚合再重编码 | 正确优先;生产版可改逐事件重编码 |
| prompt 计数 | 估算 + 厂商 usage 校准 | 没有 usage 时也能显示;有则等比缩放到真值 |
| 会话识别 | session header,否则空 | 同会话多请求才能画序列/算 headroom |
| 每轮转写 | 存增量(与上一请求 diff) | 避免整段历史重复,轮次 token 准确 |

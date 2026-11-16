# 路由与故障转移

## 1. 候选规划 (`routing.Plan`)

一个模型名可能是:路由组 `group/<id>`、`<provider>/<model>`、或裸模型名。规划把它们展开成一条**候选链**。

```mermaid
flowchart TB
  Q(["Plan(model, session)"]) --> G{"是路由组?<br/>group/id 或裸 id 命中组"}
  G -->|是| PG["planGroup: 展开每个 member<br/>→ (provider, model, 各账号)"]
  G -->|否| PM{"含 '/' 且前缀是 provider?"}
  PM -->|是| PP["planProvider(provider, model)"]
  PM -->|否| Bare["裸模型: 遍历 provider,<br/>default_model 匹配者展开"]
  PG --> Order
  PP --> Order
  Bare --> Order
  Order["order(cands, strategy)<br/>应用策略 + 账号亲和/冷却"]
```

## 2. 账号展开与亲和/冷却

每个 provider 展开成「每个账号一个候选」。顺序:

```mermaid
flowchart LR
  A["provider 的账号列表"] --> Split{"该账号?"}
  Split -->|"被冷却(resting)"| Back["排到最后"]
  Split -->|"= 会话亲和的账号"| Front["排到最前"]
  Split -->|其他| Mid["按账号顺序"]
  Front --> Result
  Mid --> Result
  Back --> Result
```

- **会话亲缘 (affinity)**:`Served(session, provider, account)` 记住会话上次由谁回答,
  `providerCandidates` 把该账号提前 → 保住厂商的 **prompt cache**。
- **冷却 (rest)**:某个账号 429/402/5xx 失败后 `Rest(30s)`,后续请求先避开它。

## 3. 策略 (`order`)

```mermaid
flowchart TB
  S{"strategy"} -->|order| O["保持规划顺序"]
  S -->|rotate| R["把第一个挪到末尾(轮询)"]
  S -->|usage| U["按账号已服务次数升序"]
  S -->|weighted| W["按 provider.Weight 降序"]
```

## 4. 故障转移循环

```mermaid
stateDiagram-v2
  [*] --> TryCandidate
  TryCandidate --> Success: 2xx
  TryCandidate --> Retryable: 429 / 402 / 5xx / 网络错误
  TryCandidate --> Fatal: 其他 4xx
  Retryable --> Rest: Rest(账号, 30s)
  Rest --> Next: 还有候选
  Retryable --> Next: 还有候选
  Next --> TryCandidate: 下一个候选
  Next --> AllFailed: 候选耗尽
  Fatal --> ReturnErr: 写回客户端
  AllFailed --> [*]: 502
  Success --> [*]: 记录 Tried + 服务
  ReturnErr --> [*]
```


记录里 `Tried[]` 保留每次尝试的 `(provider, account, status)`,仪表盘可看「谁先失败、谁最终回答」。

## 5. 示例

配置:
```jsonc
"groups": [{ "id": "daily", "strategy": "order", "members": [
  { "provider": "bad",  "model": "m" },
  { "provider": "good", "model": "m" }
]}]
```

请求 `model: "group/daily"`:

```mermaid
sequenceDiagram
  participant G
  participant Bad
  participant Good
  G->>Bad: tryOne(m)
  Bad-->>G: 503 (可重试)
  G->>G: Rest("bad", 30s)  记录 Tried[bad,503]
  G->>Good: tryOne(m)
  Good-->>G: 200 "Hello world"
  G-->>G: served, Tried=[bad:503], provider=good
```

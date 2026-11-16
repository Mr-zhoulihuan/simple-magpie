# 上下文窗口仪表盘原理

## 1. 数据从哪来

```mermaid
flowchart LR
  Body["请求 body"] --> Parse["protocol.Parse → IR Request"]
  Parse --> Analyze["prompt.AnalyzeRequest"]
  Analyze --> Parts["按来源归类 + 累加 token"]
  Parts --> Est["Prompt{Parts[], Tokens(估算), Turns}"]
  Reply["上游回复 usage"] --> Cal["Prompt.Calibrate(真实总量)"]
  Est --> Cal
  Cal --> Rec["store.Record.Prompt"]
  Rec --> API["/dashboard/session"]
  API --> UI["treemap + 图例 + 明细"]
```

## 2. prompt 解剖:6 个分类

`prompt.AnalyzeRequest` 遍历 IR 消息,把每块内容按**来源**归类:

| 分类 (kind) | 来源 | 判定 |
| --- | --- | --- |
| `system` 系统提示词 | system/developer 消息 | 默认 |
| `tools` 工具 | 工具定义 (name+desc+schema) | 每个工具一项 |
| `memory` 记忆 | 含 `AGENTS.md`/`CLAUDE.md`/`memory`/`<reminder>` 的文本 | `classifyText` |
| `files` 文件 | `read`/`view`/`grep`/`context7` 等工具的结果、图片 | `readsFile` |
| `results` 工具结果 | shell/bash 等工具的其它输出 | 其余工具结果 |
| `chat` 会话内容 | 用户与模型的话、模型的调用 | 剩余文本/调用 |

每个分类内按 item(文件名/工具名)聚合,最大的 24 个保留,其余折叠成「另外 N 项」。

## 3. token 估算与校准

```mermaid
flowchart TB
  T["TokensOf(s)"] --> A["ASCII: ceil(len/4)"]
  T --> CJK["非 ASCII(CJK): 1 字符 = 1 token"]
  Img["图片"] --> IT["固定 ~1600 token"]
  Total["Prompt.Tokens = 各部分之和(估算)"]
  Total --> Q{"有厂商 usage?"}
  Q -->|有| Scale["Calibrate(real):<br/>比例 r = real/估算<br/>每 part/item × r,Tokens=real,Counted=true"]
  Q -->|无| Keep["保持估算,Counted=false"]
```

## 4. 缓存命中

```mermaid
flowchart LR
  U["usage{input, cache_read, cache_write}"] --> Hit["CacheHit = cache_read /(input+cache_read+cache_write)"]
```

仪表盘右上的「提示词缓存 48% 命中 / 2.9K 来自缓存 · 3.1K 新增」即来自这里。

## 5. 剩余请求数 (`headroom`)

用该会话每轮 token 的**相邻增量中位数**估算还能问几轮:

```mermaid
flowchart TB
  Series["每轮 token 序列"] --> Steps["取相邻增量 d=series[i]-series[i-1]"]
  Steps --> Filter["过滤: d>0 且 本轮≥上轮×0.6(忽略压缩/掉落的轮)"]
  Filter --> Med["取中位数 g"]
  Med --> HN["headroom = floor(剩余空间 / g)"]
```

中位数抗异常:某轮超大的工具输出不会把估算拉偏。

## 6. treemap 布局

`squarify` 用 squarified-treemap 算法,把矩形按 token 占比切块:

```mermaid
flowchart LR
  R["整块矩形 = 上下文窗口"] --> Frac["已用部分按 share^0.35 缓曲线<br/>(小用量也能看清)"]
  Frac --> Sq["squarify: 依短边逐行铺块<br/>面积 ∝ token"]
  Sq --> Free["剩余空间 → 斜线填充块"]
  Sq --> Label["块内: 分类名 + 顶部 item"]
```

## 7. 页面结构

```mermaid
flowchart TB
  Head["顶部指标:<br/>已用 X/Y token · 已用% · 还能容纳 N · 缓存% 命中"] --> Tree["treemap 树状矩形"]
  Tree --> Legend["图例: 6 分类 + 剩余空间"]
  Legend --> Tabs["内容分类 tab / 对话内容 tab"]
  Tabs --> Rows["明细表: 按分类筛选,每行一个 item + 占比"]
  Tabs --> Conv["对话内容: 按轮次(见 conversation.md)"]
```

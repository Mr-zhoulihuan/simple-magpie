# 对话内容视图原理

## 1. 转写捕获

对话由两部分拼成:**请求携带的消息** + **本请求的回复**。

```mermaid
sequenceDiagram
  participant G as gateway.handle
  participant TR as transcript
  participant ST as store
  G->>TR: FromRequest(req) 把 IR 转成可展示 turns
  G->>ST: PriorTurnCount(session) 上一请求的完整轮数
  G->>G: rec.Turns = full[prior:]  ← 只保留本轮新增
  G->>TR: AppendReply(rec.Turns, text+calls)  服务成功后
  G->>ST: Add(rec)
```

**为什么存增量**:Agent 每轮都会把**完整历史**重新发来。若每轮都存全量,仪表盘会重复显示整段历史。
用与上一请求的 diff(`prior` = 上条的 `AllCount`),本轮只留新增的消息 + 本轮回复,轮次 token 也才准确。

## 2. 转写数据模型

```mermaid
classDiagram
  class Turn {
    +string Role
    +string Kind
    +string Text
    +string CallID
    +string Name
    +string Args
    +bool IsError
    +bool Trunc
    +time Time
  }
```

`transcript.FromRequest` 按 IR `Part.Kind` 映射:
`text→text`、`thinking→thinking`、`tool_call→tool_call`、`tool_result→tool_result`(role 记为 `tool`)。

## 3. 存储聚合 (`store.Turns`)

```mermaid
flowchart TB
  Recs["某会话的所有记录(按时间)"]
  Recs --> Loop["按顺序遍历"]
  Loop --> Each["每条记录 = 一轮(turn)"]
  Each --> Items["items = 该记录新增的 turns(过滤无名 tool_call 碎片)"]
  Items --> Num["index = 1,2,3...(轮次号)"]
  Num --> Out["Turn{Index, Time, Tokens, Items}"]
  Out --> API["GET /dashboard/turns"]
```

`Tokens` 来自该记录的 `Prompt.Tokens`(已按 usage 校准)。

## 4. 前端展示

```mermaid
flowchart TB
  Tabs["对话内容 tab"] --> List["轮次列表"]
  List --> Group["按日期分组: 今天 / 昨天 / YYYY-MM-DD"]
  Group --> Row["每轮一行: ▸ 第 N 轮 · 时间 · token"]
  Row --> Click{"点击轮次"}
  Click --> Body["展开该轮 items"]
  Body --> It1["系统: 系统提示词"]
  Body --> It2["你: 用户消息(蓝)"]
  Body --> It3["工具调用 <名>: 格式化 JSON(琥珀)"]
  Body --> It4["工具结果: 真实输出"]
  Body --> It5["思考: 推理片段(灰斜体)"]
  Body --> It6["助手: 回复(紫)"]
  It1 & It2 & It3 & It4 & It5 & It6 --> Copy["每块右上角 ⧉ 复制"]
  List --> AllBtn["全部展开 / 全部折叠"]
```

## 5. 关键交互

| 交互 | 实现 |
| --- | --- |
| 切换会话 | 顶部下拉 `/dashboard/sessions` → 选 `session` |
| 按日期分组 | `dayOf(time)` → 今天/昨天/日期 |
| 折叠轮次 | `openTurn` 记录当前展开的轮次号;点击表头切换 |
| 复制内容 | `navigator.clipboard.writeText`,按钮临时变 ✓ |
| 直链 | `#conv` 直接打开对话视图 |

# simple-magpie 文档

本目录用图与文字讲清 simple-magpie 的**架构**与**细节流程原理**。

| 文档 | 内容 |
| --- | --- |
| [architecture.md](architecture.md) | 总体架构:分层、包职责、完整请求数据流 |
| [request-flow.md](request-flow.md) | 一次请求从进来到返回的完整时序(含 relay / translate / 故障转移) |
| [communication.md](communication.md) | **通讯流程图**:HTTP/SSE 线级交换、Header 变换、流式中转与重编码 |
| [protocol-ir.md](protocol-ir.md) | 统一中间表示 (IR) 与四协议互转原理 |
| [routing.md](routing.md) | 路由与故障转移:候选规划、策略、账号冷却、会话亲缘 |
| [context-window.md](context-window.md) | 上下文窗口仪表盘:prompt 解剖、token 估算、treemap、headroom |
| [conversation.md](conversation.md) | 对话内容视图:按轮次、日期分组、折叠、转写捕获 |
| [api.md](api.md) | HTTP API 一览 |

> 所有图用 [Mermaid](https://mermaid.js.org) 编写,GitHub / Gitee / VS Code 均可直接渲染。

## 一句话概括

simple-magpie 是一个**本地 LLM 网关**:每个编码 Agent 把 `base_url` 指向它,它把请求路由到某个
provider/账号,能同协议就**原样转发(relay)**,否则经**统一 IR 互转(translate)**;
同时把每个请求的 **prompt 组成**与**对话/工具调用**记录下来,用仪表盘可视化。

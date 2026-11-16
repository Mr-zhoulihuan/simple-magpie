# HTTP API 一览

## 网关(给 Agent 用)

| 方法 | 路径 | 协议 | 说明 |
| --- | --- | --- | --- |
| POST | `/v1/chat/completions`, `/chat/completions` | OpenAI Chat | 聊天补全 |
| POST | `/v1/responses`, `/responses` | OpenAI Responses | 响应 API |
| POST | `/v1/messages`, `/messages` | Anthropic Messages | Claude 客户端 |
| POST | `/v1beta/models/{model}:generateContent` | Gemini | Gemini 客户端 |
| GET | `/v1/models` | — | 可用模型(providers + groups) |
| GET | `/` | — | 端点说明 |

请求模型可写:`group/<id>`、`<provider>/<model>`、裸模型名。
会话 header:`X-Magpie-Session` / `session_id` / `conversation_id`。

## 仪表盘(给浏览器用)

| 方法 | 路径 | 返回 |
| --- | --- | --- |
| GET | `/dashboard` | 仪表盘页面(可加 `#conv`) |
| GET | `/dashboard/session?session=` | 单会话上下文统计 `{window,tokens,parts,usage,cacheHit,headroom,series,turns}` |
| GET | `/dashboard/turns?session=` | 单会话按轮次 `{turns:[{index,time,tokens,items[]}]}` |
| GET | `/dashboard/transcript?session=` | 单会话拍平转写 `{turns:[Turn]}` |
| GET | `/dashboard/sessions` | 所有会话 `{sessions:[{session,agent,model,requests,tokens}]}` |
| GET | `/dashboard/recent?n=` | 最近请求 `{records:[...]}` |
| GET | `/dashboard/record?id=` | 单条记录完整 JSON |

## 数据结构

```jsonc
// Prompt (session.prompt)
{
  "window": 1048576, "tokens": 5929, "counted": true, "turns": 1,
  "parts": [
    { "kind": "tools",  "tokens": 2418, "items": [{ "name": "bash", "tokens": 324, "n": 1 }] },
    { "kind": "memory", "tokens": 3248, "items": [{ "name": "AGENTS.md", "tokens": 203 }] }
  ]
}

// Turn (turns[].items[])
{ "role": "assistant", "kind": "tool_call", "name": "bash",
  "callId": "c1", "args": "{\"command\":\"ls\"}", "time": "2026-11-16T11:52:00Z" }
```

// Package gateway is simple-magpie's local LLM gateway: it serves the wire
// APIs agents speak (OpenAI Chat, OpenAI Responses, Anthropic Messages,
// Gemini), routes each request to a provider/account, relays it as-is when the
// provider speaks the same API or translates between APIs via the IR, and
// records what each prompt held for the dashboard.
package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"simple-magpie/internal/config"
	"simple-magpie/internal/prompt"
	"simple-magpie/internal/protocol"
	"simple-magpie/internal/routing"
	"simple-magpie/internal/store"
	"simple-magpie/internal/transcript"
)

// Token is the bearer token agents are told to use. The gateway listens on
// loopback and accepts anything, but agents insist on one.
const Token = "smagpie"

// Server is the gateway.
type Server struct {
	cfg    *config.Config
	router *routing.Router
	store  *store.Store
	client *http.Client
}

// New makes a server.
func New(cfg *config.Config, st *store.Store) *Server {
	return &Server{
		cfg:    cfg,
		router: routing.New(cfg),
		store:  st,
		client: &http.Client{Timeout: 0}, // streams may be long; no overall deadline
	}
}

// Handler returns the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/chat/completions", s.handle)
	mux.HandleFunc("POST /chat/completions", s.handle)
	mux.HandleFunc("POST /v1/responses", s.handle)
	mux.HandleFunc("POST /responses", s.handle)
	mux.HandleFunc("POST /v1/messages", s.handle)
	mux.HandleFunc("POST /messages", s.handle)
	mux.HandleFunc("POST /v1beta/models/{call...}", s.handle)
	mux.HandleFunc("GET /", s.index)
	return mux
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, 200, map[string]any{"name": "simple-magpie", "token": Token,
		"endpoints": []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1beta/models/{model}:generateContent", "/dashboard"}})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	var data []map[string]any
	for _, id := range s.cfg.ProviderIDs() {
		p, _ := s.cfg.Provider(id)
		if p.DefaultModel != "" {
			data = append(data, map[string]any{"id": id + "/" + p.DefaultModel, "object": "model", "owned_by": id})
		}
	}
	for i := range s.cfg.Groups {
		g := &s.cfg.Groups[i]
		data = append(data, map[string]any{"id": "group/" + g.ID, "object": "model", "owned_by": "group"})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

// handle serves one request: parse model, plan candidates, try each.
// 处理单个请求的完整生命周期:识别 → 旁路解剖 → 选路 → 逐个尝试 → 记录。
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()             // 请求开始时间,结束时算 Millis 指标
	body, err := io.ReadAll(r.Body) // 读完整请求体(要取模型名/解剖/转换,必须全量读入)
	if err != nil {                 // body 都读不进来
		writeError(w, protocol.Chat, 400, err.Error()) // 此刻还不知道协议,先用 Chat 格式回 400
		return
	}
	from := protocol.Detect(r.URL.Path) // 按路径判定入站协议:/messages→anthropic、/responses→responses、:generateContent→gemini、其余→chat
	// Gemini keeps the model in the path; move it into the body so Plan sees it.
	// Gemini 特例:模型名在 URL 里而不在 body,塞进 body.model 后续逻辑就统一了
	if from == protocol.Gemini {
		body = geminiModelFromPath(r.URL.Path, body) // 从 path 提取模型名写进 body
	}
	model := modelOf(body)  // gjson 取 body.model(此时四种协议都有该字段)
	session := sessionOf(r) // 从 session_id / X-Magpie-Session 等 header 读会话 id,读不到为空串
	rec := &store.Record{   // 开始构造本请求的记录(仪表盘的数据源)
		Agent: agentOf(r), Session: session, Model: model, // agent 名按 User-Agent 猜;会话与 agent 是两个独立字段
		Protocol: string(from), Streamed: streamOf(body), // 入站协议、请求是否要流式
	}
	// analyze the prompt once, before translation.
	// 旁路解剖:在转换前做,且只做一次
	rec.Prompt = prompt.Analyze(from, body)              // 解析成 IR 后按 6 类(system/tools/memory/files/results/chat)估算 token
	rec.Prompt = analyzeWithWindow(rec.Prompt, model, s) // 按模型名查内置表,填 Prompt.Window(上下文窗口)
	// the conversation the request carried. Only the messages new since the
	// session's last request are kept as this turn's content, so the dashboard
	// shows each turn once, not the whole history every time.
	// 转写增量:Agent 每轮都重发全量历史,只保留与上一请求的差量,仪表盘才不会重复显示整段历史
	if req, err := protocol.Parse(from, body); err == nil { // 解析成 IR 成功才做转写
		full := transcript.FromRequest(req) // 把请求携带的全部内容拍平成可展示 turns
		rec.AllCount = len(full)            // 本请求总共带了多少条对话内容
		start := 0                          // 注意:变量遮蔽,这里是 int 下标,遮蔽外层 time.Time 的 start
		if prior := s.store.PriorTurnCount(session); prior > 0 && prior <= len(full) {
			start = prior // prior = 该会话上一条记录的 AllCount(上一轮带了几条),从这里截断
		}
		rec.Turns = full[start:] // 只留本轮新增部分
	}

	cands, resolved := s.router.Plan(model, session) // 选路:group/id、provider/model、裸名 → 候选链(亲和置顶/冷却置底/策略排序);resolved 为解析后的模型名
	if len(cands) == 0 {                             // 没有任何 provider/model 能接这个请求
		rec.Status = 404                                                          // 记 404
		rec.Err = "no provider serves " + model                                   // 记失败原因
		rec.Millis = time.Since(start).Milliseconds()                             // 耗时
		s.store.Add(rec)                                                          // 失败也落库,仪表盘可见
		writeError(w, from, 404, "no provider serves model "+strconvQuote(model)) // 按 from 协议的格式写回错误
		return
	}
	rec.Model = resolved // 记录里存解析后的模型名(如请求 group/daily → 存 group/daily)

	// try candidates in order; move on for a retryable failure before any body
	// has been sent to the client.
	// 按序尝试候选;可重试失败发生在向客户端写任何字节之前,所以换候选不会产生半截输出
	for i, c := range cands { // 逐个尝试,谁先成功用谁
		p, ok := s.cfg.Provider(c.Provider) // 取该候选的 provider 配置
		if !ok {                            // 配置里查不到(防御性分支)
			continue // 跳过该候选
		}
		status, served, retryable := s.tryOne(w, r, from, p, c, body, rec) // 一次尝试:relay 或 translate 并写回客户端;返回 状态码/是否已服务/是否可换下一个
		if served {                                                        // 已成功把回复写给客户端
			rec.Provider, rec.Account, rec.Status = c.Provider, c.Account.Name, status // 记最终由哪个 provider/账号、什么状态回答
			rec.Millis = time.Since(start).Milliseconds()                              // 总耗时
			// append the assistant's answer for this request to the transcript
			// only real tool calls (a streamed fragment with no name is not one)
			// 只把真正的 tool_call 追加进转写:流式期间收到的无名分片不是完整调用
			calls := rec.ReplyCalls[:0]        // 复用底层数组原地过滤(长度归零,容量不变)
			for _, c := range rec.ReplyCalls { // 注意:内层 c 遮蔽外层候选 c
				if c.Name != "" { // 没有 name 的是流式增量碎片
					calls = append(calls, c) // 只保留完整调用
				}
			}
			rec.ReplyCalls = calls                              // 覆盖回记录
			if rec.ReplyText != "" || len(rec.ReplyCalls) > 0 { // 回复有内容才追加
				rec.Turns = transcript.AppendReply(rec.Turns, transcript.ReplyTurn{Text: rec.ReplyText, Calls: rec.ReplyCalls}) // 助手回复拼成本轮转写的最后一块
			}
			// calibrate the prompt's estimate to the vendor's real total so the
			// dashboard's "used" and part sizes are the counted ones.
			// 用厂商真实 usage 校准估算值,仪表盘显示的才是计数结果
			if rec.Usage.Known && rec.Usage.Input+rec.Usage.CacheRead+rec.Usage.CacheWrite > 0 { // 厂商报了 usage 才校准
				rec.Prompt.Calibrate(rec.Usage.Input + rec.Usage.CacheRead + rec.Usage.CacheWrite) // 真实总量 = input+缓存读+缓存写,各分类等比缩放到真值
			}
			rec.CacheHit = rec.Usage.CacheHit()                  // 缓存命中率 = cache_read / 总量
			s.router.Served(session, c.Provider, c.Account.Name) // 记亲和(该会话下次优先回此账号)+ 账号服务计数
			s.store.Add(rec)                                     // 落库
			return                                               // 成功即结束
		}
		rec.Tried = append(rec.Tried, store.Try{Provider: c.Provider, Account: c.Account.Name, Status: status, Err: rec.Err}) // 失败:记进 Tried 链(谁先失败、什么状态)
		if !retryable {                                                                                                       // 不可重试(其他 4xx,错误体已透传给客户端)
			break // 直接停止,不换候选
		}
		if i < len(cands)-1 { // 只有后面还有候选时才冷却(最后一个失败没必要 Rest)
			// brief rest so the next request avoids this account
			s.router.Rest(c.Provider, c.Account.Name, 30*time.Second) // 该账号休息 30s,后续请求把它排到最后
		}
	}
	// every candidate failed: the last error already written, or write one.
	// 全部候选失败:通常最后一个错误已写回;此分支兜底「一个字节都没写过」的边缘情况(如候选全被 continue 跳过)
	if rec.Status == 0 { // 还没记过状态
		rec.Status = 502 // 记 502
	}
	rec.Millis = time.Since(start).Milliseconds() // 总耗时
	s.store.Add(rec)                              // 失败记录也落库
}

// tryOne makes one attempt. served is true when the reply was written to w.
// retryable is true when the failure is one another candidate may handle.
func (s *Server) tryOne(w http.ResponseWriter, r *http.Request, from protocol.Protocol, p *config.Provider, c routing.Candidate, body []byte, rec *store.Record) (status int, served, retryable bool) {
	// choose the upstream API: the same one if the provider speaks it, else the
	// first the provider speaks.
	to := from
	if !p.Speaks(string(to)) {
		to = firstProtocol(p)
	}
	rec.Upstream = string(to)

	var sendBody []byte
	relay := p.Speaks(string(from))
	if relay {
		sendBody = body
	} else {
		req, err := protocol.Parse(from, body)
		if err != nil {
			rec.Status = 400
			rec.Err = err.Error()
			writeError(w, from, 400, err.Error())
			return 400, false, false
		}
		sendBody, err = protocol.Build(to, req, c.Model)
		if err != nil {
			rec.Status = 500
			rec.Err = err.Error()
			writeError(w, from, 500, err.Error())
			return 500, false, false
		}
	}
	// the model field: keep the provider's own model name (c.Model)
	sendBody = rewriteModel(sendBody, c.Model)

	url := protocol.UpstreamPath(p.BaseURL, to, c.Model, streamOf(sendBody))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(sendBody))
	if err != nil {
		rec.Status = 502
		rec.Err = err.Error()
		writeError(w, from, 502, err.Error())
		return 502, false, true
	}
	copyHeaders(req.Header, r.Header, to, c.Account.Key)
	if s := r.Header.Get("session_id"); s != "" {
		req.Header.Set("session_id", s)
	}

	res, err := s.client.Do(req)
	if err != nil {
		rec.Status = 502
		rec.Err = err.Error()
		writeError(w, from, 502, p.Name+": "+err.Error())
		return 502, false, true
	}
	defer res.Body.Close()

	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		rec.Status = res.StatusCode
		rec.Err = string(b)
		retry := res.StatusCode == 429 || res.StatusCode == 402 || res.StatusCode >= 500
		if !retry {
			// a client error: pass it through as the answer
			copyPassHeaders(w.Header(), res.Header)
			w.WriteHeader(res.StatusCode)
			w.Write(b)
		}
		return res.StatusCode, !retry, retry
	}

	// relay a same-API reply as-is; translate a different-API reply.
	if relay {
		s.relay(res, w, from, rec)
	} else {
		s.translateReply(res, w, from, to, rec)
	}
	return res.StatusCode, true, false
}

// firstProtocol is the first API a provider speaks.
func firstProtocol(p *config.Provider) protocol.Protocol {
	if len(p.Protocols) > 0 {
		return protocol.Protocol(p.Protocols[0])
	}
	return protocol.Chat
}

func copyHeaders(dst, src http.Header, to protocol.Protocol, key string) {
	for k, vs := range src {
		lk := strings.ToLower(k)
		if isHop(lk) || lk == "host" || lk == "content-length" || lk == "authorization" || lk == "x-api-key" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	switch to {
	case protocol.Anthropic:
		dst.Set("x-api-key", key)
		dst.Set("anthropic-version", orDefault(src.Get("anthropic-version"), "2023-06-01"))
	case protocol.Gemini:
		dst.Set("x-goog-api-key", key)
	default:
		dst.Set("Authorization", "Bearer "+key)
	}
}

func copyPassHeaders(dst, src http.Header) {
	for k, vs := range src {
		if isHop(strings.ToLower(k)) {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func isHop(k string) bool {
	switch k {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "content-encoding":
		return true
	}
	return false
}

func orDefault(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, proto protocol.Protocol, status int, msg string) {
	switch proto {
	case protocol.Anthropic:
		writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
	default:
		writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "api_error"}})
	}
}

func strconvQuote(s string) string { return "\"" + s + "\"" }

// logf logs a line a vendor said, for debugging.
func logf(format string, a ...any) { log.Printf(format, a...) }

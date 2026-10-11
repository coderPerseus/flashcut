// Package llm talks to chat models over the common protocols, so users can
// bring their own service: OpenAI-compatible APIs (OpenAI, relays, DeepSeek,
// Alibaba Bailian/DashScope, Ollama…), Anthropic, and Cloudflare Workers AI.
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"flashcut/internal/clef"
)

// Protocols.
const (
	OpenAI    = "openai"
	Anthropic = "anthropic"
	WorkersAI = "workers-ai"
)

// Provider is a configured model service.
type Provider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseUrl"`
	APIKey   string `json:"apiKey,omitempty"`
	// Models are the models the user picked or fetched.
	Models []string `json:"models"`
	// Builtin marks the Cloudflare entry that uses the app's Clef connection.
	Builtin bool `json:"builtin"`
}

// Message is a chat message. Images are JPEG bytes sent with user messages.
type Message struct {
	Role       string     `json:"role"` // system, user, assistant, tool
	Content    string     `json:"content"`
	Images     [][]byte   `json:"-"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
}

// Tool is a function the model may call. Parameters is a JSON Schema.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is a call the model asked for.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Request is one chat completion.
type Request struct {
	Messages  []Message
	Tools     []Tool
	MaxTokens int
	// JSON asks for a JSON object reply where the protocol supports it.
	JSON bool
}

// Response is the model's reply.
type Response struct {
	Text      string        `json:"text"`
	ToolCalls []ToolCall    `json:"toolCalls"`
	Latency   time.Duration `json:"latency"`
	Model     string        `json:"model"`
}

// Client sends requests; Cloudflare runs through the app's Clef client.
type Client struct {
	HTTP *http.Client
	// Cloudflare runs Workers AI models; nil disables that protocol.
	Cloudflare *clef.Client
}

// New returns a client with sensible timeouts.
func New(cf *clef.Client) *Client {
	return &Client{HTTP: &http.Client{Timeout: 3 * time.Minute}, Cloudflare: cf}
}

// Chat sends req to model on p.
func (c *Client) Chat(ctx context.Context, p Provider, model string, req Request) (*Response, error) {
	if model == "" {
		return nil, errors.New("还没有选择模型")
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 1024
	}
	start := time.Now()
	var (
		resp *Response
		err  error
	)
	switch p.Protocol {
	case Anthropic:
		resp, err = c.anthropic(ctx, p, model, req)
	case WorkersAI:
		resp, err = c.workersAI(ctx, model, req)
	default:
		resp, err = c.openAI(ctx, p, model, req)
	}
	if err != nil {
		return nil, err
	}
	resp.Latency = time.Since(start)
	resp.Model = model
	resp.Text = cleanText(resp.Text)
	return resp, nil
}

var reThink = regexp.MustCompile(`(?s)<think>.*?</think>`)

// cleanText drops reasoning blocks some models put in the reply.
func cleanText(s string) string {
	return strings.TrimSpace(reThink.ReplaceAllString(s, ""))
}

// ---- OpenAI-compatible ----

func (c *Client) openAIBody(model string, req Request, workers bool) map[string]any {
	var msgs []map[string]any
	for _, m := range req.Messages {
		msg := map[string]any{"role": m.Role}
		if len(m.Images) > 0 {
			parts := []map[string]any{{"type": "text", "text": m.Content}}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img)}})
			}
			msg["content"] = parts
		} else {
			msg["content"] = m.Content
		}
		if len(m.ToolCalls) > 0 {
			var calls []map[string]any
			for _, tc := range m.ToolCalls {
				calls = append(calls, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": string(tc.Arguments)}})
			}
			msg["tool_calls"] = calls
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		msgs = append(msgs, msg)
	}
	body := map[string]any{"model": model, "messages": msgs, "max_tokens": req.MaxTokens}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters}})
		}
		body["tools"] = tools
	}
	if req.JSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if workers {
		// Qwen 3 thinks by default; replies here should be direct.
		body["chat_template_kwargs"] = map[string]bool{"enable_thinking": false}
		delete(body, "model")
	}
	return body
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments any    `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	// Some Workers AI models answer in this older shape.
	Response  any `json:"response"`
	ToolCalls []struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"tool_calls"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (r *openAIResponse) toResponse() (*Response, error) {
	if r.Error != nil {
		return nil, errors.New(r.Error.Message)
	}
	out := &Response{}
	if len(r.Choices) > 0 {
		m := r.Choices[0].Message
		switch {
		case m.Content != nil && *m.Content != "":
			out.Text = *m.Content
		case m.ReasoningContent != nil && len(m.ToolCalls) == 0:
			// Workers AI puts the reply here when thinking is off.
			out.Text = *m.ReasoningContent
		}
		for i, tc := range m.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: id, Name: tc.Function.Name, Arguments: rawArgs(tc.Function.Arguments)})
		}
		return out, nil
	}
	switch v := r.Response.(type) {
	case string:
		out.Text = v
	case nil:
	default:
		b, _ := json.Marshal(v)
		out.Text = string(b)
	}
	for i, tc := range r.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: fmt.Sprintf("call_%d", i), Name: tc.Name, Arguments: rawArgs(tc.Arguments)})
	}
	if out.Text == "" && len(out.ToolCalls) == 0 {
		return nil, errors.New("模型返回了空结果")
	}
	return out, nil
}

// rawArgs accepts arguments as a JSON string or an object.
func rawArgs(v any) json.RawMessage {
	if s, ok := v.(string); ok {
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(v)
	return b
}

func (c *Client) openAI(ctx context.Context, p Provider, model string, req Request) (*Response, error) {
	data, err := c.post(ctx, joinURL(p.BaseURL, "/chat/completions"), map[string]string{"Authorization": "Bearer " + p.APIKey}, c.openAIBody(model, req, false))
	if err != nil {
		return nil, err
	}
	var r openAIResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("无法解析回复：%.200s", data)
	}
	return r.toResponse()
}

func (c *Client) workersAI(ctx context.Context, model string, req Request) (*Response, error) {
	if c.Cloudflare == nil {
		return nil, errors.New("还没有连接 Cloudflare：在设置里填入 Account ID 和 API Token，或者在终端运行 cf auth login")
	}
	data, err := c.Cloudflare.RunRaw(ctx, model, c.openAIBody(model, req, true))
	if err != nil {
		return nil, err
	}
	var r openAIResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("无法解析回复：%.200s", data)
	}
	return r.toResponse()
}

// ---- Anthropic ----

func (c *Client) anthropic(ctx context.Context, p Provider, model string, req Request) (*Response, error) {
	var system []string
	var msgs []map[string]any
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			system = append(system, m.Content)
		case "tool":
			msgs = append(msgs, map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content}}})
		default:
			var content []map[string]any
			for _, img := range m.Images {
				content = append(content, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": "image/jpeg", "data": base64.StdEncoding.EncodeToString(img)}})
			}
			if m.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input any
				_ = json.Unmarshal(tc.Arguments, &input)
				content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input})
			}
			msgs = append(msgs, map[string]any{"role": m.Role, "content": content})
		}
	}
	body := map[string]any{"model": model, "max_tokens": req.MaxTokens, "messages": msgs}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Parameters})
		}
		body["tools"] = tools
	}
	data, err := c.post(ctx, joinURL(p.BaseURL, "/messages"), map[string]string{"x-api-key": p.APIKey, "anthropic-version": "2023-06-01"}, body)
	if err != nil {
		return nil, err
	}
	var r struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("无法解析回复：%.200s", data)
	}
	if r.Error != nil {
		return nil, errors.New(r.Error.Message)
	}
	out := &Response{}
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			out.Text += b.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: b.Input})
		}
	}
	return out, nil
}

// ---- models ----

// ListModels fetches the model ids the service offers.
func (c *Client) ListModels(ctx context.Context, p Provider) ([]string, error) {
	if p.Protocol == WorkersAI {
		return WorkersAIModels, nil
	}
	headers := map[string]string{"Authorization": "Bearer " + p.APIKey}
	if p.Protocol == Anthropic {
		headers = map[string]string{"x-api-key": p.APIKey, "anthropic-version": "2023-06-01"}
	}
	data, err := c.get(ctx, joinURL(p.BaseURL, "/models"), headers)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("无法解析模型列表：%.200s", data)
	}
	var ids []string
	for _, m := range r.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// WorkersAIModels are chat models on Cloudflare that suit this app.
var WorkersAIModels = []string{
	"@cf/qwen/qwen3-30b-a3b-fp8",
	"@cf/qwen/qwen3.8-27b",
	"@cf/zai-org/glm-4.7-flash",
	"@cf/zai-org/glm-5.3-flash",
	"@cf/deepseek-ai/deepseek-v4-flash-0731",
	"@cf/meta/llama-3.2-11b-vision-instruct",
}

// ---- HTTP ----

func joinURL(base, path string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + path
}

func (c *Client) post(ctx context.Context, url string, headers map[string]string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, http.MethodPost, url, headers, payload)
}

func (c *Client) get(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, url, headers, nil)
}

func (c *Client) do(ctx context.Context, method, url string, headers map[string]string, payload []byte) ([]byte, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.New("Base URL 需要以 http:// 或 https:// 开头")
	}
	var lastErr error
	for attempt := range 3 {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("连不上 %s：%w", req.URL.Host, err)
		} else {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case resp.StatusCode < 300:
				return data, nil
			case resp.StatusCode == 401 || resp.StatusCode == 403:
				return nil, fmt.Errorf("密钥无效或没有权限（HTTP %d）：%.200s", resp.StatusCode, data)
			case resp.StatusCode == 404:
				return nil, fmt.Errorf("地址不对（HTTP 404），检查 Base URL 是否包含 /v1：%s", url)
			case resp.StatusCode == 429 || resp.StatusCode >= 500:
				lastErr = fmt.Errorf("服务暂时不可用（HTTP %d）：%.200s", resp.StatusCode, data)
			default:
				return nil, fmt.Errorf("HTTP %d：%.300s", resp.StatusCode, data)
			}
		}
		select {
		case <-time.After(time.Duration(1<<attempt) * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

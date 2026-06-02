// Package mcp 是 Model Context Protocol 客户端的零依赖实现。
//
// 只实现 Streamable HTTP transport（MCP spec 2025-06-18）的客户端侧，无鉴权
// （见 ADR-0024）。手写 JSON-RPC over HTTP，不引第三方 SDK（ADR-0019）。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const protocolVersion = "2025-06-18"

// Tool 是 MCP server 暴露的一个工具。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Client 是单个 MCP server 的连接。非并发安全：一个 turn 内顺序使用。
type Client struct {
	endpoint  string
	http      *http.Client
	sessionID string
	nextID    int
	authz     string // 可选 Authorization 头值（如 "Bearer xxx"）
}

// NewClient 创建指向 endpoint 的 MCP 客户端。
func NewClient(endpoint string) *Client {
	return &Client{
		endpoint: endpoint,
		http:     &http.Client{Timeout: 60 * time.Second},
	}
}

// SetAuthorization 设置每个请求带的 Authorization 头（如 vault static_bearer，ADR-0026）。
func (c *Client) SetAuthorization(v string) { c.authz = v }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message) }

// Initialize 走 MCP 握手：initialize → notifications/initialized。
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "jadeenvoy", "version": "0.1"},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	// initialized 通知（无 id），期望 202/200。
	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return fmt.Errorf("initialized notify: %w", err)
	}
	return nil
}

// ListTools 返回 server 暴露的工具。
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return out.Tools, nil
}

// CallToolResult 是 tools/call 的结果（content 块 + 是否错误）。
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Text 把 content 拼成纯文本（供回喂给模型）。
func (r CallToolResult) Text() string {
	var sb strings.Builder
	for _, b := range r.Content {
		if b.Text != "" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// CallTool 调用 server 上的工具。
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}
	var res CallToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode tools/call: %w", err)
	}
	return &res, nil
}

// call 发一个有 id 的 JSON-RPC 请求并返回 result。
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	body, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})

	resp, err := c.do(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 首次 initialize 后捕获 session id。
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, string(raw))
	}

	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "text/event-stream"):
		return readRPCFromSSE(resp.Body, id)
	default:
		var rr rpcResponse
		if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
			return nil, fmt.Errorf("decode json response: %w", err)
		}
		if rr.Error != nil {
			return nil, rr.Error
		}
		return rr.Result, nil
	}
}

// notify 发一个无 id 的通知（不期望 result）。
func (c *Client) notify(ctx context.Context, method string) error {
	body, _ := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method})
	resp, err := c.do(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("mcp notify http %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if c.authz != "" {
		req.Header.Set("Authorization", c.authz)
	}
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	return c.http.Do(req)
}

// readRPCFromSSE 从 SSE 流里读出 id 匹配的 JSON-RPC response。
func readRPCFromSSE(body io.Reader, wantID int) (json.RawMessage, error) {
	scanner := bufio.NewScanner(body)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var rr rpcResponse
		if err := json.Unmarshal([]byte(data), &rr); err != nil {
			continue // 跳过非 JSON-RPC 的 SSE 事件
		}
		if rr.ID == nil || *rr.ID != wantID {
			continue // server→client 请求/通知，或别的 id，跳过
		}
		if rr.Error != nil {
			return nil, rr.Error
		}
		return rr.Result, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("sse read: %w", err)
	}
	return nil, fmt.Errorf("sse stream ended without response for id %d", wantID)
}

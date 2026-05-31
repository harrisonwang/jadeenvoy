// Command je 是 JadeEnvoy 的 CLI 客户端，通过 REST 与 jed 通信。
//
// 故意用 stdlib flag 自己做子命令派发，不引 spf13/cobra —— 遵循 ADR-0019 的零第三方
// 依赖原则（ADR-0018 原计划 cobra，本实现修订为 stdlib）。
//
// 配置走环境变量：
//
//	JE_API      jed base URL，默认 http://localhost:8787
//	JE_API_KEY  x-api-key（required/optional 模式下需要）
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	c := newClient()
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Printf("je %s (%s)\n", version.Version, version.Commit)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	case "health":
		return c.getPrint("/health")
	case "agents":
		return c.agents(args[1:])
	case "sessions":
		return c.sessions(args[1:])
	case "vaults":
		return c.vaults(args[1:])
	case "apikeys":
		return c.apikeys(args[1:])
	default:
		return fmt.Errorf("unknown command %q (try `je help`)", args[0])
	}
}

func usage() {
	fmt.Print(`je — JadeEnvoy CLI

Usage:
  je <command> [args]

Env:
  JE_API       jed base URL (default http://localhost:8787)
  JE_API_KEY   x-api-key for required/optional auth modes

Commands:
  version
  health
  agents list
  agents get <id>
  agents create --name N --model M [--system S|@file]
  agents delete <id>
  sessions create --agent <id> [--title T]
  sessions list
  sessions get <id>
  sessions send <id> <message...>      stream the agent turn
  vaults list
  vaults create --name N
  vaults cred-add <vaultID> --url URL --token TOK [--name N]
  vaults cred-list <vaultID>
  apikeys create [--name N]
  apikeys list
`)
}

// ─── client ───────────────────────────────────────────────────────────────

type client struct {
	base string
	key  string
	hc   *http.Client
}

func newClient() *client {
	base := os.Getenv("JE_API")
	if base == "" {
		base = "http://localhost:8787"
	}
	return &client{
		base: strings.TrimRight(base, "/"),
		key:  os.Getenv("JE_API_KEY"),
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *client) do(method, path string, body any) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("x-api-key", c.key)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, nil
}

// getPrint GET 并 pretty-print JSON 响应。
func (c *client) getPrint(path string) error { return c.callPrint("GET", path, nil) }

func (c *client) callPrint(method, path string, body any) error {
	raw, code, err := c.do(method, path, body)
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("%s %s → %d: %s", method, path, code, strings.TrimSpace(string(raw)))
	}
	printJSON(raw)
	return nil
}

func printJSON(raw []byte) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Println(string(raw))
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

// ─── agents ─────────────────────────────────────────────────────────────

func (c *client) agents(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: je agents <list|get|create|delete>")
	}
	switch args[0] {
	case "list":
		return c.getPrint("/v1/agents")
	case "get":
		if len(args) < 2 {
			return errors.New("usage: je agents get <id>")
		}
		return c.getPrint("/v1/agents/" + args[1])
	case "delete":
		if len(args) < 2 {
			return errors.New("usage: je agents delete <id>")
		}
		return c.callPrint("DELETE", "/v1/agents/"+args[1], nil)
	case "create":
		fs := flag.NewFlagSet("agents create", flag.ContinueOnError)
		name := fs.String("name", "", "agent name")
		model := fs.String("model", "", "model id")
		system := fs.String("system", "", "system prompt (or @file)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || *model == "" {
			return errors.New("agents create requires --name and --model")
		}
		sys, err := maybeReadFile(*system)
		if err != nil {
			return err
		}
		body := map[string]any{"name": *name, "model": *model}
		if sys != "" {
			body["system"] = sys
		}
		return c.callPrint("POST", "/v1/agents", body)
	default:
		return fmt.Errorf("unknown agents action %q", args[0])
	}
}

// ─── sessions ─────────────────────────────────────────────────────────────

func (c *client) sessions(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: je sessions <list|get|create|send>")
	}
	switch args[0] {
	case "list":
		return c.getPrint("/v1/sessions")
	case "get":
		if len(args) < 2 {
			return errors.New("usage: je sessions get <id>")
		}
		return c.getPrint("/v1/sessions/" + args[1])
	case "create":
		fs := flag.NewFlagSet("sessions create", flag.ContinueOnError)
		agent := fs.String("agent", "", "agent id")
		title := fs.String("title", "", "session title")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *agent == "" {
			return errors.New("sessions create requires --agent")
		}
		body := map[string]any{"agent": *agent}
		if *title != "" {
			body["title"] = *title
		}
		return c.callPrint("POST", "/v1/sessions", body)
	case "send":
		if len(args) < 3 {
			return errors.New("usage: je sessions send <id> <message...>")
		}
		return c.sessionsSend(args[1], strings.Join(args[2:], " "))
	default:
		return fmt.Errorf("unknown sessions action %q", args[0])
	}
}

// sessionsSend 发一条 user.message 然后轮询事件日志，增量打印本轮 turn 直到 idle。
// 用轮询而非 SSE：stream 端点是 live 订阅（不回放），轮询 listEvents 按 seq 增量更可靠。
func (c *client) sessionsSend(sid, msg string) error {
	maxSeq := c.maxSeq(sid)
	body := map[string]any{"events": []any{map[string]any{
		"type":    "user.message",
		"content": []any{map[string]any{"type": "text", "text": msg}},
	}}}
	if _, code, err := c.do("POST", "/v1/sessions/"+sid+"/events", body); err != nil {
		return err
	} else if code >= 300 {
		return fmt.Errorf("send failed: %d", code)
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)
		events, err := c.fetchEvents(sid)
		if err != nil {
			return err
		}
		for _, ev := range events {
			if seqOf(ev) <= maxSeq {
				continue
			}
			maxSeq = seqOf(ev)
			if printEvent(ev) {
				return nil // idle / terminated
			}
		}
	}
	return errors.New("timeout waiting for agent turn")
}

func (c *client) maxSeq(sid string) int64 {
	events, _ := c.fetchEvents(sid)
	var m int64
	for _, ev := range events {
		if s := seqOf(ev); s > m {
			m = s
		}
	}
	return m
}

func (c *client) fetchEvents(sid string) ([]map[string]any, error) {
	raw, code, err := c.do("GET", "/v1/sessions/"+sid+"/events", nil)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, fmt.Errorf("list events: %d", code)
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	sort.SliceStable(resp.Data, func(i, j int) bool { return seqOf(resp.Data[i]) < seqOf(resp.Data[j]) })
	return resp.Data, nil
}

func seqOf(ev map[string]any) int64 {
	if f, ok := ev["seq"].(float64); ok {
		return int64(f)
	}
	return 0
}

// printEvent 打印一条事件，返回 true 表示 turn 结束（idle/terminated）。
func printEvent(ev map[string]any) bool {
	switch ev["type"] {
	case "agent.message":
		for _, block := range asSlice(ev["content"]) {
			if m, ok := block.(map[string]any); ok && m["type"] == "text" {
				fmt.Println(str(m["text"]))
			}
		}
	case "agent.tool_use", "agent.custom_tool_use":
		input, _ := json.Marshal(ev["input"])
		fmt.Printf("→ %s %s\n", str(ev["name"]), string(input))
	case "agent.tool_result":
		fmt.Printf("  ⮑ %s\n", truncate(str(ev["content"]), 500))
	case "session.status_requires_action":
		fmt.Println("[requires_action — waiting for a custom tool result]")
		return true
	case "session.status_idle":
		fmt.Println("[idle]")
		return true
	case "session.status_terminated":
		fmt.Println("[terminated]")
		return true
	}
	return false
}

// ─── vaults ─────────────────────────────────────────────────────────────

func (c *client) vaults(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: je vaults <list|create|cred-add|cred-list>")
	}
	switch args[0] {
	case "list":
		return c.getPrint("/v1/vaults")
	case "create":
		fs := flag.NewFlagSet("vaults create", flag.ContinueOnError)
		name := fs.String("name", "", "vault display name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("vaults create requires --name")
		}
		return c.callPrint("POST", "/v1/vaults", map[string]any{"display_name": *name})
	case "cred-list":
		if len(args) < 2 {
			return errors.New("usage: je vaults cred-list <vaultID>")
		}
		return c.getPrint("/v1/vaults/" + args[1] + "/credentials")
	case "cred-add":
		if len(args) < 2 {
			return errors.New("usage: je vaults cred-add <vaultID> --url URL --token TOK [--name N]")
		}
		vaultID := args[1]
		fs := flag.NewFlagSet("vaults cred-add", flag.ContinueOnError)
		url := fs.String("url", "", "mcp_server_url, e.g. https://git.example.com")
		token := fs.String("token", "", "bearer token")
		name := fs.String("name", "credential", "display name")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *url == "" || *token == "" {
			return errors.New("cred-add requires --url and --token")
		}
		body := map[string]any{
			"display_name": *name,
			"auth":         map[string]any{"type": "static_bearer", "mcp_server_url": *url, "token": *token},
		}
		return c.callPrint("POST", "/v1/vaults/"+vaultID+"/credentials", body)
	default:
		return fmt.Errorf("unknown vaults action %q", args[0])
	}
}

// ─── apikeys ─────────────────────────────────────────────────────────────

func (c *client) apikeys(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: je apikeys <list|create>")
	}
	switch args[0] {
	case "list":
		return c.getPrint("/admin/api_keys")
	case "create":
		fs := flag.NewFlagSet("apikeys create", flag.ContinueOnError)
		name := fs.String("name", "", "key name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return c.callPrint("POST", "/admin/api_keys", map[string]any{"name": *name})
	default:
		return fmt.Errorf("unknown apikeys action %q", args[0])
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────

func maybeReadFile(s string) (string, error) {
	if strings.HasPrefix(s, "@") {
		b, err := os.ReadFile(s[1:])
		if err != nil {
			return "", fmt.Errorf("read system prompt file: %w", err)
		}
		return string(b), nil
	}
	return s, nil
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// 按 rune 边界截断，避免切碎多字节字符（n 视作最大字符数）
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

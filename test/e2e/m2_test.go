package e2e

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestE2E_M2_CustomTool 测试自定义工具的两阶段闭环：
//   1. agent 调 custom tool → session 进 requires_action
//   2. client 发 custom_tool_result → session 恢复 → 最终回复
func TestE2E_M2_CustomTool(t *testing.T) {
	srv, mock := setupHarness(t)

	// 配 mock：
	//   第 1 轮: LLM 调自定义工具 my_search
	//   第 2 轮（收到 custom_tool_result 后）: 回复总结
	mock.AppendToolUse("my_search", map[string]any{
		"query": "how to deploy",
	})
	mock.AppendFinalAfterTool("search results: use docker compose up")

	// 创建 agent（带自定义工具）
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "custom-tool-agent",
		"model":  "mock-model",
		"system": "test custom tools",
		"tools": []map[string]any{
			{"type": "agent_toolset_20260401"},
			{
				"type":        "custom",
				"name":        "my_search",
				"description": "Search internal knowledge base",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
					"required": []string{"query"},
				},
			},
		},
	}, &ag)
	agentID := ag["id"].(string)

	// 创建 session
	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	// 发 user message
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "how do I deploy?"}}},
		},
	}, nil)

	// 等 session 进入 requires_action（不是 idle）
	events := waitForEventType(t, srv, sessionID, "session.status_requires_action", 5*1000_000_000)

	// 验证事件序列
	assertHasEventType(t, events, "user.message")
	assertHasEventType(t, events, "session.status_running")
	assertHasEventType(t, events, "agent.custom_tool_use")
	assertHasEventType(t, events, "session.status_requires_action")

	// 验证 custom tool_use 内容
	tu := findFirstEvent(t, events, "agent.custom_tool_use")
	if tu["name"] != "my_search" {
		t.Fatalf("expected tool_use name=my_search, got %v", tu["name"])
	}
	input, _ := tu["input"].(map[string]any)
	if input["query"] != "how to deploy" {
		t.Fatalf("expected query='how to deploy', got %v", input["query"])
	}

	// 验证 session 是 requires_action
	var checkSess map[string]any
	getJSON(t, srv, "/v1/sessions/"+sessionID, &checkSess)
	if checkSess["status"] != "requires_action" {
		t.Fatalf("expected session status=requires_action, got %v", checkSess["status"])
	}

	// client 发送 custom_tool_result
	var acc map[string]any
	code := postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{
				"type":        "user.custom_tool_result",
				"tool_use_id": tu["id"],
				"content":     "search results: use docker compose up",
			},
		},
	}, &acc)
	if code != 202 {
		t.Fatalf("post custom_tool_result: %d", code)
	}

	// 等 session 回到 idle
	events2 := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 验证最终 agent.message 存在
	assertHasEventType(t, events2, "user.custom_tool_result")
	assertHasEventType(t, events2, "agent.message")
	assertHasEventType(t, events2, "session.status_idle")

	msg := findFirstEvent(t, events2, "agent.message")
	contentBlocks, _ := msg["content"].([]any)
	first, _ := contentBlocks[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "docker compose") {
		t.Fatalf("expected 'docker compose' in final message, got %v", first["text"])
	}

	// 验证 mock 被调了 2 次（第一轮 custom tool + 第二轮回复）
	if got := mock.CalledCount(); got != 2 {
		t.Fatalf("expected mock called 2 times, got %d", got)
	}
}

// waitForEventType 轮询 events 直到出现指定事件类型或超时。
func waitForEventType(t *testing.T, srv *httptest.Server, sessionID, evType string, timeoutNS int64) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(time.Duration(timeoutNS))
	for {
		var resp struct {
			Data []map[string]any `json:"data"`
		}
		getJSON(t, srv, "/v1/sessions/"+sessionID+"/events", &resp)
		for _, ev := range resp.Data {
			if ev["type"] == evType {
				return resp.Data
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for event type %q; events seen: %s", evType, summarize(resp.Data))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestE2E_M2_SkillsUploadAndInject 测试 Skills 上传→agent 绑定→system prompt 注入闭环
func TestE2E_M2_SkillsUploadAndInject(t *testing.T) {
	srv, mock := setupHarness(t)

	// 1. 创建 skill（含 SKILL.md + 附件）
	var skill map[string]any
	code := postJSON(t, srv, "/v1/skills", map[string]any{
		"name":        "deploy-guide",
		"description": "Deployment instructions",
		"files": []map[string]any{
			{
				"path":    "SKILL.md",
				"content": "When asked about deployment, always respond: Use 'docker compose up -d' to deploy.",
			},
			{
				"path":    "config.json",
				"content": `{"port": 8080, "mode": "production"}`,
			},
		},
	}, &skill)
	if code != 201 {
		t.Fatalf("create skill: %d, body=%v", code, skill)
	}
	skillID, _ := skill["id"].(string)
	if !strings.HasPrefix(skillID, "skl-") {
		t.Fatalf("expected skl- id, got %v", skillID)
	}
	if skill["name"] != "deploy-guide" {
		t.Fatalf("expected name=deploy-guide, got %v", skill["name"])
	}

	// 2. Get skill
	var got map[string]any
	code = getJSON(t, srv, "/v1/skills/"+skillID, &got)
	if code != 200 {
		t.Fatalf("get skill: %d", code)
	}

	// 3. List skills
	var list map[string]any
	getJSON(t, srv, "/v1/skills", &list)
	data, _ := list["data"].([]any)
	if len(data) == 0 {
		t.Fatal("expected at least 1 skill in list")
	}

	// 4. 创建带 skill 的 agent → session → 验证 SKILL.md 注入
	mock.AppendText("Use docker compose up -d to deploy")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "skill-agent",
		"model":  "mock-model",
		"system": "test skills",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
		"skills": []map[string]any{
			{"type": "custom", "skill_id": skillID},
		},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	// 验证 agent snapshot 含 skills
	var checkSess map[string]any
	getJSON(t, srv, "/v1/sessions/"+sessionID, &checkSess)
	agentSnap, _ := checkSess["agent"].(map[string]any)
	skillsList, _ := agentSnap["skills"].([]any)
	if len(skillsList) == 0 {
		t.Fatalf("expected skills in agent snapshot: %v", agentSnap)
	}

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "how to deploy?"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 验证 agent 回复了 SKILL.md 的内容
	msg := findFirstEvent(t, events, "agent.message")
	contentBlocks, _ := msg["content"].([]any)
	first, _ := contentBlocks[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "docker compose up -d") {
		t.Fatalf("expected 'docker compose up -d' in response (SKILL.md injected), got %v", first["text"])
	}

	// 5. Delete skill
	code = deleteReq(t, srv, "/v1/skills/"+skillID)
	if code != 200 {
		t.Fatalf("delete skill: %d", code)
	}
}

// TestE2E_M2_SkillsZipUpload 测试通过 multipart ZIP 上传 skill。
func TestE2E_M2_SkillsZipUpload(t *testing.T) {
	srv, mock := setupHarness(t)

	// 创建 ZIP 文件（SKILL.md + data.txt）
	zipBuf := &bytes.Buffer{}
	zw := zip.NewWriter(zipBuf)
	// SKILL.md
	fw, _ := zw.Create("SKILL.md")
	fw.Write([]byte("When asked about auth, respond: Use OAuth 2.0 with PKCE."))
	// data.txt
	fw2, _ := zw.Create("data.txt")
	fw2.Write([]byte("Sensitive data here"))
	zw.Close()

	// Multipart upload
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	// name field
	w.WriteField("name", "auth-guide")
	// file field
	fw3, _ := w.CreateFormFile("file", "auth-guide.zip")
	fw3.Write(zipBuf.Bytes())
	w.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/v1/skills", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("upload zip: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		t.Fatalf("upload zip: %d, body=%s", resp.StatusCode, raw)
	}
	var skill map[string]any
	json.Unmarshal(raw, &skill)
	skillID := skill["id"].(string)

	// 验证 files
	files, _ := skill["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("expected 2 files in skill, got %d: %v", len(files), skill)
	}

	// 用 agent 验证 SKILL.md 注入
	mock.AppendText("Use OAuth 2.0 with PKCE")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "zip-skill-agent",
		"model":  "mock-model",
		"system": "test",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
		"skills": []map[string]any{{"type": "custom", "skill_id": skillID}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "how to auth?"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)
	msg := findFirstEvent(t, events, "agent.message")
	contentBlocks, _ := msg["content"].([]any)
	first, _ := contentBlocks[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "OAuth 2.0") {
		t.Fatalf("expected SKILL.md content injected via zip upload, got %v", first["text"])
	}
}

// TestE2E_M2_FileTools 测试 write + read 工具的两轮 agent 闭环。
//
// 模拟：用户问"创建并读 hello.txt"
//   轮 1: agent 用 write 工具创建 /workspace/hello.txt
//   轮 2: agent 收到 write 成功，用 read 工具读
//   轮 3: agent 收到内容，回复用户
func TestE2E_M2_FileTools(t *testing.T) {
	srv, mock := setupHarness(t)

	// 脚本: 三轮（write → read → final text）
	mock.Append(scriptForWrite()).
		Append(scriptForRead()).
		AppendFinalAfterTool("file content is 'hello jadeenvoy m2'")

	// agent + session
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "filer",
		"model":  "mock-model",
		"system": "test files",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID, _ := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{"agent": agentID}, &sess)
	sessionID, _ := sess["id"].(string)

	// 触发
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "create and read"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 验证：两个 tool_use（write 和 read）+ 两个 tool_result
	toolUses := filterByType(events, "agent.tool_use")
	if len(toolUses) != 2 {
		t.Fatalf("expected 2 tool_use events, got %d: %s", len(toolUses), summarize(events))
	}
	if toolUses[0]["name"] != "write" {
		t.Fatalf("first tool should be write, got %v", toolUses[0]["name"])
	}
	if toolUses[1]["name"] != "read" {
		t.Fatalf("second tool should be read, got %v", toolUses[1]["name"])
	}

	// 验证 read 的 tool_result 含写入的内容
	results := filterByType(events, "agent.tool_result")
	if len(results) != 2 {
		t.Fatalf("expected 2 tool_results, got %d", len(results))
	}
	readResult := results[1]["content"].(string)
	if !strings.Contains(readResult, "hello jadeenvoy m2") {
		t.Fatalf("read result should contain written content, got: %q", readResult)
	}
}

// TestE2E_M2_MemoryStore 测试 memory store CRUD 端点。
func TestE2E_M2_MemoryStore(t *testing.T) {
	srv, _ := setupHarness(t)

	// 创建 store
	var store map[string]any
	code := postJSON(t, srv, "/v1/memory_stores", map[string]any{
		"name":        "user-prefs",
		"description": "Per-user preferences",
	}, &store)
	if code != 201 {
		t.Fatalf("create store: %d, body=%v", code, store)
	}
	storeID, _ := store["id"].(string)
	if !strings.HasPrefix(storeID, "mst-") {
		t.Fatalf("expected mst- id, got %v", storeID)
	}

	// 写一条 memory
	var mem map[string]any
	code = postJSON(t, srv, "/v1/memory_stores/"+storeID+"/memories", map[string]any{
		"path":    "/formatting.md",
		"content": "Always use tabs.",
	}, &mem)
	if code != 201 {
		t.Fatalf("upsert memory: %d, body=%v", code, mem)
	}

	// 同 path upsert（不报 409）
	code = postJSON(t, srv, "/v1/memory_stores/"+storeID+"/memories", map[string]any{
		"path":    "/formatting.md",
		"content": "Use spaces.",
	}, nil)
	if code != 201 {
		t.Fatalf("upsert overwrite: %d", code)
	}

	// 列出
	var list map[string]any
	code = getJSON(t, srv, "/v1/memory_stores/"+storeID+"/memories", &list)
	if code != 200 {
		t.Fatalf("list memories: %d", code)
	}
	data, _ := list["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected 1 memory after upsert, got %d", len(data))
	}

	// 获取
	memID := data[0].(map[string]any)["id"].(string)
	var retrieved map[string]any
	code = getJSON(t, srv, "/v1/memory_stores/"+storeID+"/memories/"+memID, &retrieved)
	if code != 200 {
		t.Fatalf("get memory: %d", code)
	}
	if retrieved["content"] != "Use spaces." {
		t.Fatalf("expected content=Use spaces., got %v", retrieved["content"])
	}
}

// TestE2E_M2_MemoryMountToSession 测试 session 挂载 memory_store + agent
// 通过 read 工具读到 memory 内容。
func TestE2E_M2_MemoryMountToSession(t *testing.T) {
	srv, mock := setupHarness(t)

	// 创建 store + 预填内容
	var store map[string]any
	postJSON(t, srv, "/v1/memory_stores", map[string]any{
		"name": "myprefs",
	}, &store)
	storeID := store["id"].(string)
	postJSON(t, srv, "/v1/memory_stores/"+storeID+"/memories", map[string]any{
		"path":    "/style.md",
		"content": "magic-number-42",
	}, nil)

	// agent + session（带 memory_store resource）
	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "reader",
		"model":  "mock-model",
		"system": "test memory mount",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	code := postJSON(t, srv, "/v1/sessions", map[string]any{
		"agent": agentID,
		"resources": []map[string]any{
			{
				"type":            "memory_store",
				"memory_store_id": storeID,
				"access":          "read_only",
			},
		},
	}, &sess)
	if code != 201 {
		t.Fatalf("create session w/ resources: %d, body=%v", code, sess)
	}
	sessionID := sess["id"].(string)

	// 配 mock: agent 用 read 工具读挂载的文件
	mock.AppendToolUse("read", map[string]any{
		"path": "/mnt/memory/myprefs/style.md",
	}).AppendFinalAfterTool("found: magic-number-42")

	// 触发
	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "what's in memory?"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 验证 read 工具读到 memory 内容
	results := filterByType(events, "agent.tool_result")
	if len(results) == 0 {
		t.Fatalf("expected tool_result, got events: %s", summarize(events))
	}
	content := results[0]["content"].(string)
	if !strings.Contains(content, "magic-number-42") {
		t.Fatalf("expected read tool result to contain 'magic-number-42', got: %q", content)
	}
}

// ─── 脚本辅助 ─────────────────────────────────────────────────────────────

func scriptForWrite() provider_MockScript {
	return provider_MockScript{
		Match: func(req provider_ChatRequest) bool {
			for _, m := range req.Messages {
				for _, b := range m.Content {
					if b.Type == "tool_result" {
						return false
					}
				}
			}
			return true
		},
		Events: []provider_ChatEvent{
			provider_ToolUseStart{ID: "call_w1", Name: "write"},
			provider_ToolUseDelta{ID: "call_w1", InputJSON: `{"path":"/workspace/hello.txt","content":"hello jadeenvoy m2"}`},
			provider_StopReason{Reason: "tool_use", Usage: provider_Usage{InputTokens: 10, OutputTokens: 5}},
		},
	}
}

func scriptForRead() provider_MockScript {
	return provider_MockScript{
		Match: func(req provider_ChatRequest) bool {
			// 已有 write 的 tool_result 但没有 read 的 → 这一轮用 read
			seenResults := 0
			for _, m := range req.Messages {
				for _, b := range m.Content {
					if b.Type == "tool_result" {
						seenResults++
					}
				}
			}
			return seenResults == 1
		},
		Events: []provider_ChatEvent{
			provider_ToolUseStart{ID: "call_r1", Name: "read"},
			provider_ToolUseDelta{ID: "call_r1", InputJSON: `{"path":"/workspace/hello.txt"}`},
			provider_StopReason{Reason: "tool_use", Usage: provider_Usage{InputTokens: 20, OutputTokens: 5}},
		},
	}
}

// filterByType 返回 events 中所有指定 type 的事件。
func filterByType(events []map[string]any, t string) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		if ev["type"] == t {
			out = append(out, ev)
		}
	}
	return out
}

// uploadFile 帮助函数：multipart 上传一个文件。
func uploadFile(t *testing.T, srv *httptest.Server, filename, content string) map[string]any {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write([]byte(content))
	_ = w.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/v1/files", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		t.Fatalf("upload: %d, body=%s", resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode upload: %v, body=%s", err, raw)
	}
	return out
}

// deleteReq sends a DELETE request to a path.
func deleteReq(t *testing.T, srv *httptest.Server, path string) int {
	t.Helper()
	req, _ := http.NewRequest("DELETE", srv.URL+path, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestE2E_M2_FileUploadAndMount 测试 Files API 完整链路：
//   上传 → metadata → list → content → session 挂载 → agent read → 删除
func TestE2E_M2_FileUploadAndMount(t *testing.T) {
	srv, mock := setupHarness(t)

	// 1. 上传文件
	fileContent := "# Q1 Report\nRevenue: $10M\n"
	uploaded := uploadFile(t, srv, "report.md", fileContent)
	fileID, _ := uploaded["id"].(string)
	if !strings.HasPrefix(fileID, "fil-") {
		t.Fatalf("expected fil- id, got %v", fileID)
	}

	// 2. Get file metadata
	var meta map[string]any
	code := getJSON(t, srv, "/v1/files/"+fileID, &meta)
	if code != 200 {
		t.Fatalf("get file: %d", code)
	}
	if meta["filename"] != "report.md" {
		t.Fatalf("expected filename=report.md, got %v", meta["filename"])
	}

	// 3. List files
	var filesList map[string]any
	getJSON(t, srv, "/v1/files", &filesList)
	data, _ := filesList["data"].([]any)
	if len(data) == 0 {
		t.Fatal("expected at least 1 file in list")
	}

	// 4. Get file content
	resp, err := srv.Client().Get(srv.URL + "/v1/files/" + fileID + "/content")
	if err != nil {
		t.Fatalf("get content: %v", err)
	}
	defer resp.Body.Close()
	content, _ := io.ReadAll(resp.Body)
	if string(content) != fileContent {
		t.Fatalf("content mismatch: got=%q, want=%q", content, fileContent)
	}

	// 5. Session 挂载 file → agent 用 read 工具读
	mock.AppendToolUse("read", map[string]any{
		"path": "/mnt/session/report.md",
	}).AppendFinalAfterTool("Revenue is $10M")

	var ag map[string]any
	postJSON(t, srv, "/v1/agents", map[string]any{
		"name":   "file-reader",
		"model":  "mock-model",
		"system": "test files",
		"tools":  []map[string]any{{"type": "agent_toolset_20260401"}},
	}, &ag)
	agentID := ag["id"].(string)

	var sess map[string]any
	postJSON(t, srv, "/v1/sessions", map[string]any{
		"agent": agentID,
		"resources": []map[string]any{
			{"type": "file", "file_id": fileID, "mount_path": "/mnt/session/report.md"},
		},
	}, &sess)
	sessionID := sess["id"].(string)

	postJSON(t, srv, "/v1/sessions/"+sessionID+"/events", map[string]any{
		"events": []map[string]any{
			{"type": "user.message", "content": []map[string]any{{"type": "text", "text": "read report"}}},
		},
	}, nil)

	events := waitForIdle(t, srv, sessionID, 10*1000_000_000)

	// 验证 read 工具的 tool_result 含内容
	results := filterByType(events, "agent.tool_result")
	if len(results) == 0 {
		t.Fatalf("expected tool_result: %s", summarize(events))
	}
	resContent := results[0]["content"].(string)
	if !strings.Contains(resContent, "$10M") {
		t.Fatalf("expected '$10M' in read result, got: %q", resContent)
	}

	// 6. Delete file
	code = deleteReq(t, srv, "/v1/files/"+fileID)
	if code != 200 {
		t.Fatalf("delete file: %d", code)
	}
}

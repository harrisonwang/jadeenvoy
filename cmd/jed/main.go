package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/agent"
	"github.com/harrisonwang/jadeenvoy/internal/api"
	"github.com/harrisonwang/jadeenvoy/internal/auth"
	"github.com/harrisonwang/jadeenvoy/internal/config"
	"github.com/harrisonwang/jadeenvoy/internal/event"
	"github.com/harrisonwang/jadeenvoy/internal/harness"
	"github.com/harrisonwang/jadeenvoy/internal/memory"
	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/internal/provider"
	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
	"github.com/harrisonwang/jadeenvoy/internal/session"
	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/internal/tool"
	"github.com/harrisonwang/jadeenvoy/internal/vault"
	"github.com/harrisonwang/jadeenvoy/internal/version"
	"github.com/harrisonwang/jadeenvoy/internal/webhook"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	log := obs.Logger()
	log.Info("jed.starting",
		"version", version.Version, "commit", version.Commit,
		"http_addr", cfg.HTTPAddr, "data_dir", cfg.DataDir,
		"auth_mode", cfg.AuthMode, "llm_provider", cfg.LLMProvider,
		"sandbox", cfg.SandboxKind)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Store
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Broker
	broker := event.NewBroker(st)

	// 启动恢复：进程上次崩溃时仍在 running/rescheduling 的 session 没人接管，
	// 标记 terminated 并补发终态事件（保持 event log 真相源），避免永久僵尸。
	recoverInterruptedSessions(ctx, st, broker, log)

	// Provider
	prov, err := buildProvider(cfg)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}

	// Sandbox
	sbProvider := sandbox.NewLocalSubprocessProvider(filepath.Join(cfg.DataDir, "sandboxes"))
	sbProvider.KeepWorkdir = cfg.SandboxKeepWorkdir

	// Tools
	registry := tool.NewRegistry()
	registry.Register(tool.BashTool{})
	registry.Register(tool.ReadTool{})
	registry.Register(tool.WriteTool{})
	registry.Register(tool.EditTool{})
	registry.Register(tool.GlobTool{})
	registry.Register(tool.GrepTool{})

	// Services
	agentSvc := agent.New(st)
	sessionSvc := session.New(st)
	memorySvc := memory.New(st)
	webhookSvc := webhook.New(st)
	webhookSvc.AllowPrivate = cfg.WebhookAllowPrivate
	vaultSvc, err := vault.New(st, cfg.PlatformRootSecret)
	if err != nil {
		return fmt.Errorf("vault service: %w", err)
	}
	authSvc := auth.New(st, cfg.AuthMode, []byte(cfg.PlatformRootSecret))
	if cfg.PlatformRootSecret == "" && cfg.AuthMode != "bypass" {
		log.Warn("platform_root_secret.unset",
			"msg", "PLATFORM_ROOT_SECRET not set; using insecure dev key for vault encryption + cookie signing")
	}
	hrns := harness.New(st, broker, prov, sbProvider, registry)
	hrns.Memory = memorySvc
	hrns.VaultProxyURL = cfg.VaultProxyURL
	hrns.VaultCACert = cfg.VaultCACert
	hrns.CompactThresholdTokens = cfg.CompactThresholdTokens
	hrns.KeepRecentTurns = cfg.KeepRecentTurns
	hrns.MaxRetries = cfg.LLMMaxRetries
	hrns.RetryBackoff = time.Duration(cfg.LLMRetryBackoffMS) * time.Millisecond
	// MCP 静态鉴权（ADR-0026）：按 host 解析 vault static_bearer 注入 Authorization。
	hrns.VaultResolveToken = func(ctx context.Context, tenantID string, vaultIDs []string, host string) string {
		rc, err := vaultSvc.Resolve(ctx, tenantID, vaultIDs, host)
		if err != nil || rc == nil {
			return ""
		}
		return rc.Token
	}

	// 把 broker 事件路由给 webhook 投递队列（异步 enqueue）
	broker.RegisterHook(func(ev event.Event) {
		go func() {
			bg := context.Background()
			if err := webhookSvc.PublishEvent(bg, "tnt-default", ev); err != nil {
				log.Warn("webhook.publish.failed", "err", err.Error())
			}
		}()
	})

	// 启动 webhook 投递 worker
	go webhookSvc.Run(ctx)

	// HTTP
	deps := &api.Deps{
		Store:             st,
		Broker:            broker,
		Agent:             agentSvc,
		Session:           sessionSvc,
		Memory:            memorySvc,
		Webhook:           webhookSvc,
		Vault:             vaultSvc,
		Auth:              authSvc,
		Harness:           hrns,
		AuthMode:          cfg.AuthMode,
		LLMProvider:       cfg.LLMProvider,
		DefaultAgentModel: cfg.DefaultAgentModel,
	}
	r := api.NewRouter(deps)
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("jed.listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("jed.shutting_down")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	return srv.Shutdown(shutdownCtx)
}

func buildProvider(cfg *config.Config) (provider.Provider, error) {
	switch cfg.LLMProvider {
	case "mock":
		// 默认 mock 没有 script，每次返回 "no matching script" + end_turn
		return provider.NewMockProvider(), nil
	case "openai_compat":
		if cfg.LLMBaseURL == "" {
			return nil, fmt.Errorf("JE_LLM_BASE_URL required for openai_compat provider")
		}
		return provider.NewOAICompat(cfg.LLMBaseURL, cfg.LLMAPIKey), nil
	case "anthropic":
		// base_url 空 → 默认 api.anthropic.com（官方）
		return provider.NewAnthropic(cfg.LLMBaseURL, cfg.LLMAPIKey, "anthropic"), nil
	case "anthropic_compat":
		if cfg.LLMBaseURL == "" {
			return nil, fmt.Errorf("JE_LLM_BASE_URL required for anthropic_compat provider")
		}
		return provider.NewAnthropic(cfg.LLMBaseURL, cfg.LLMAPIKey, "anthropic_compat"), nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.LLMProvider)
	}
}

// recoverInterruptedSessions 把上次进程未干净结束、仍处于 running/rescheduling 的 session
// 标记 terminated，并通过 broker 补发 session.status_terminated 事件（保持 event log 真相源）。
func recoverInterruptedSessions(ctx context.Context, st *store.Store, broker *event.Broker, log *slog.Logger) {
	ids, err := st.ListSessionsByStatus(ctx, "running", "rescheduling")
	if err != nil {
		log.Warn("recover.list.failed", "err", err.Error())
		return
	}
	for _, id := range ids {
		if err := st.MarkSessionTerminated(ctx, id); err != nil {
			log.Warn("recover.mark.failed", "session_id", id, "err", err.Error())
			continue
		}
		payload, _ := json.Marshal(map[string]any{
			"type": "session.status_terminated",
			"error": map[string]string{
				"type":    "interrupted",
				"message": "session was interrupted by a daemon restart",
			},
		})
		if _, err := broker.Publish(ctx, id, "session.status_terminated", "primary", payload); err != nil {
			log.Warn("recover.publish.failed", "session_id", id, "err", err.Error())
		}
	}
	if len(ids) > 0 {
		log.Info("recover.done", "terminated_sessions", len(ids))
	}
}

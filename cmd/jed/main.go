package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/agent"
	"github.com/harrisonwang/jadeenvoy/internal/api"
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

	// Provider
	prov, err := buildProvider(cfg)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}

	// Sandbox
	sbProvider := sandbox.NewLocalSubprocessProvider(filepath.Join(cfg.DataDir, "sandboxes"))

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
	hrns := harness.New(st, broker, prov, sbProvider, registry)
	hrns.Memory = memorySvc

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
		Store:    st,
		Broker:   broker,
		Agent:    agentSvc,
		Session:  sessionSvc,
		Memory:   memorySvc,
		Webhook:  webhookSvc,
		Harness:  hrns,
		AuthMode: cfg.AuthMode,
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
	case "anthropic", "anthropic_compat":
		return nil, fmt.Errorf("%s provider not implemented in V1; set JE_LLM_PROVIDER=mock", cfg.LLMProvider)
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.LLMProvider)
	}
}

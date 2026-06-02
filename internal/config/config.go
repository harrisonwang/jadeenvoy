package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config 是 jed 启动时加载的全部运行配置。
type Config struct {
	// HTTP
	HTTPAddr string // listen addr，默认 :8787

	// 存储
	DataDir     string // 数据目录绝对路径
	DatabaseURL string // sqlite:///path 或 postgres://...

	// Auth
	AuthMode string // required / optional / bypass

	// LLM provider
	LLMProvider       string // anthropic / anthropic_compat / openai_compat / mock
	LLMBaseURL        string
	LLMAPIKey         string
	DefaultAgentModel string

	// Sandbox
	SandboxKind        string // subprocess / docker
	SandboxKeepWorkdir bool   // true 则 session 删除时保留 workdir（调试）。默认 false

	// Harness — context compaction（ADR-0021）
	CompactThresholdTokens int // 历史超此 token 触发摘要压缩；<=0 关闭。默认 150000
	KeepRecentTurns        int // 压缩时逐字保留最近多少个 turn。默认 3

	// Harness — 错误恢复（ADR-0022）
	LLMMaxRetries     int // 瞬时错误重试次数。默认 2
	LLMRetryBackoffMS int // 重试退避基数（毫秒），实际退避 = base*attempt。默认 500

	// Webhook
	WebhookAllowPrivate bool // 放行私网/环回 webhook 目标（内网部署）。默认 false 防 SSRF

	// Vault MITM 代理（ADR-0006/0019）
	VaultProxyAddr string // je-vault 监听地址，默认 :14322
	VaultProxyURL  string // jed 注入沙箱的 HTTPS_PROXY（空 = 不启用注入）
	VaultCACert    string // 注入沙箱的 CA 证书路径（SSL_CERT_FILE 等）

	// 加密
	PlatformRootSecret string // AES-GCM key 来源
}

// Load 从环境变量加载，做基本校验 + 设默认。
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:    envOr("JE_HTTP_ADDR", ":8787"),
		DataDir:     envOr("JE_DATA_DIR", "./data"),
		DatabaseURL: os.Getenv("JE_DATABASE_URL"),
		AuthMode:    envOr("JE_AUTH_MODE", "bypass"),

		LLMProvider:       envOr("JE_LLM_PROVIDER", "mock"),
		LLMBaseURL:        os.Getenv("JE_LLM_BASE_URL"),
		LLMAPIKey:         os.Getenv("JE_LLM_API_KEY"),
		DefaultAgentModel: envOr("JE_DEFAULT_AGENT_MODEL", "tw-agent-max"),

		SandboxKind:        envOr("JE_SANDBOX_KIND", "subprocess"),
		SandboxKeepWorkdir: envOr("JE_SANDBOX_KEEP_WORKDIR", "") == "1",

		CompactThresholdTokens: envInt("JE_COMPACT_THRESHOLD_TOKENS", 150000),
		KeepRecentTurns:        envInt("JE_KEEP_RECENT_TURNS", 3),

		LLMMaxRetries:     envInt("JE_LLM_MAX_RETRIES", 2),
		LLMRetryBackoffMS: envInt("JE_LLM_RETRY_BACKOFF_MS", 500),

		WebhookAllowPrivate: envOr("JE_WEBHOOK_ALLOW_PRIVATE", "") == "1",

		VaultProxyAddr: envOr("JE_VAULT_PROXY_ADDR", ":14322"),
		VaultProxyURL:  os.Getenv("JE_VAULT_PROXY_URL"),
		VaultCACert:    os.Getenv("JE_VAULT_CA_CERT"),

		PlatformRootSecret: os.Getenv("PLATFORM_ROOT_SECRET"),
	}

	// 解析路径
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}
	c.DataDir = abs

	// DatabaseURL 默认 sqlite under data dir
	if c.DatabaseURL == "" {
		c.DatabaseURL = "sqlite://" + filepath.Join(c.DataDir, "jadeenvoy.db")
	}

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Validate() error {
	switch c.AuthMode {
	case "required", "optional", "bypass":
	default:
		return errors.New("JE_AUTH_MODE must be one of: required, optional, bypass")
	}
	switch c.LLMProvider {
	case "anthropic", "anthropic_compat", "openai_compat", "mock":
	default:
		return errors.New("JE_LLM_PROVIDER must be one of: anthropic, anthropic_compat, openai_compat, mock")
	}
	switch c.SandboxKind {
	case "subprocess", "docker":
	default:
		return errors.New("JE_SANDBOX_KIND must be one of: subprocess, docker")
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envInt 解析 env 为 int，失败 fallback。
func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

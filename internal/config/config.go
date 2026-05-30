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
	LLMProvider string // anthropic / anthropic_compat / openai_compat / mock
	LLMBaseURL  string
	LLMAPIKey   string

	// Sandbox
	SandboxKind string // subprocess / docker

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

		LLMProvider: envOr("JE_LLM_PROVIDER", "mock"),
		LLMBaseURL:  os.Getenv("JE_LLM_BASE_URL"),
		LLMAPIKey:   os.Getenv("JE_LLM_API_KEY"),

		SandboxKind: envOr("JE_SANDBOX_KIND", "subprocess"),

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

// 防 "declared but not used"
var _ = envInt

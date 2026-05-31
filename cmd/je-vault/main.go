// Command je-vault 是 vault 凭据注入的 HTTPS MITM 代理（ADR-0006/0019）。
//
// 沙箱出站设 HTTPS_PROXY=http://<sessionID>:x@<addr>，代理据 session 解析 vault
// 凭据，剥离客户端 dummy 凭据并注入真 token。纯 stdlib，无第三方 proxy 库。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/config"
	"github.com/harrisonwang/jadeenvoy/internal/obs"
	"github.com/harrisonwang/jadeenvoy/internal/store"
	"github.com/harrisonwang/jadeenvoy/internal/vault"
	"github.com/harrisonwang/jadeenvoy/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("je-vault %s (%s)\n", version.Version, version.Commit)
		return
	}
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	vaultSvc, err := vault.New(st, cfg.PlatformRootSecret)
	if err != nil {
		return fmt.Errorf("vault service: %w", err)
	}

	ca, err := vault.LoadOrCreateCA(filepath.Join(cfg.DataDir, "je-vault-ca"))
	if err != nil {
		return fmt.Errorf("vault CA: %w", err)
	}

	// inject: sessionID → session.vault_ids → 按 host 解析凭据
	inject := func(ctx context.Context, sessionID, host string) (string, bool) {
		if sessionID == "" {
			return "", false
		}
		sess, err := st.GetSession(ctx, sessionID)
		if err != nil {
			return "", false
		}
		var vaultIDs []string
		if err := json.Unmarshal(sess.VaultIDs, &vaultIDs); err != nil {
			log.Warn("je-vault.vault_ids.unmarshal_failed", "session_id", sessionID, "err", err.Error())
			return "", false
		}
		if len(vaultIDs) == 0 {
			return "", false
		}
		rc, err := vaultSvc.Resolve(ctx, sess.TenantID, vaultIDs, host)
		if err != nil || rc == nil {
			return "", false
		}
		return rc.Token, true
	}

	proxy := vault.NewProxy(ca, inject, nil)
	srv := &http.Server{Addr: cfg.VaultProxyAddr, Handler: proxy}

	log.Info("je-vault.starting",
		"version", version.Version, "commit", version.Commit,
		"addr", cfg.VaultProxyAddr, "ca_cert", ca.CertPath)

	errCh := make(chan error, 1)
	go func() {
		log.Info("je-vault.listening", "addr", cfg.VaultProxyAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("je-vault.shutting_down")
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	return srv.Shutdown(shutdownCtx)
}

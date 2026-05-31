// Package auth 自实现 cookie session + API key 认证（ADR-0013）。
//
// 不引外部 auth 库。密码用 stdlib crypto/pbkdf2（Go 1.24+），cookie 用 HMAC-SHA256
// 签名，API key 存 sha256。AUTH_MODE 三档 required/optional/bypass 解决 OMA
// AUTH_DISABLED 卸载整个路由的坑（oma-gaps 第 4 条）。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/harrisonwang/jadeenvoy/internal/store"
)

const (
	ModeRequired = "required"
	ModeOptional = "optional"
	ModeBypass   = "bypass"

	cookieName    = "jadeenvoy_session"
	pbkdf2Iter    = 120_000
	pbkdf2KeyLen  = 32
	defaultMaxAge = 24 * time.Hour
)

// ErrInvalidCredentials 用于登录失败（用户不存在或密码错），不区分以防枚举。
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrConflict 透传 email 已存在。
var ErrConflict = store.ErrConflict

type Service struct {
	st     *store.Store
	secret []byte
	mode   string
	maxAge time.Duration
}

// New 构造 Service。secret 来自 PLATFORM_ROOT_SECRET（用于 HMAC cookie 签名）；
// 为空时用 dev 默认（调用方负责告警）。
func New(st *store.Store, mode string, secret []byte) *Service {
	if len(secret) == 0 {
		secret = []byte("jadeenvoy-dev-insecure-cookie-secret")
	}
	if mode == "" {
		mode = ModeBypass
	}
	return &Service{st: st, secret: secret, mode: mode, maxAge: defaultMaxAge}
}

func (s *Service) Mode() string          { return s.mode }
func (s *Service) CookieName() string    { return cookieName }
func (s *Service) MaxAge() time.Duration { return s.maxAge }

// ─── Signup / Login / Logout ────────────────────────────────────────────────

func (s *Service) Signup(ctx context.Context, email, password, name string) (*store.UserRow, error) {
	if email == "" || password == "" {
		return nil, fmt.Errorf("email and password are required")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	return s.st.CreateUser(ctx, store.CreateUserInput{
		TenantID:     "tnt-default",
		Email:        email,
		Name:         name,
		PasswordHash: hash,
	})
}

// Login 验证密码，创建 auth_session，返回 (signedCookie, user)。
func (s *Service) Login(ctx context.Context, email, password string) (string, *store.UserRow, error) {
	u, err := s.st.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, ErrInvalidCredentials
		}
		return "", nil, err
	}
	if !verifyPassword(password, u.PasswordHash) {
		return "", nil, ErrInvalidCredentials
	}
	sessionID := store.NewID("ses")
	expires := time.Now().UTC().Add(s.maxAge).UnixMilli()
	if err := s.st.CreateAuthSession(ctx, sessionID, u.ID, u.TenantID, expires); err != nil {
		return "", nil, err
	}
	return s.signCookie(sessionID), u, nil
}

// Logout 解析 cookie 并删除对应 session（幂等）。
func (s *Service) Logout(ctx context.Context, signedCookie string) error {
	sessionID, ok := s.verifyCookie(signedCookie)
	if !ok {
		return nil
	}
	return s.st.DeleteAuthSession(ctx, sessionID)
}

// ResolveSession 从签名 cookie 解出并校验有效（含过期）的 session。
func (s *Service) ResolveSession(ctx context.Context, signedCookie string) (*store.AuthSessionRow, *store.UserRow, error) {
	sessionID, ok := s.verifyCookie(signedCookie)
	if !ok {
		return nil, nil, ErrInvalidCredentials
	}
	sess, err := s.st.GetAuthSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if time.Now().UTC().UnixMilli() > sess.ExpiresAt {
		_ = s.st.DeleteAuthSession(ctx, sessionID)
		return nil, nil, ErrInvalidCredentials
	}
	u, err := s.st.GetUser(ctx, sess.UserID)
	if err != nil {
		return nil, nil, err
	}
	return sess, u, nil
}

// ─── API keys ─────────────────────────────────────────────────────────────

// IssueAPIKey 生成新 key，返回 (row, plaintext)。plaintext 仅此一次可见。
func (s *Service) IssueAPIKey(ctx context.Context, tenantID, userID, name string) (*store.APIKeyRow, string, error) {
	plaintext, prefix := generateAPIKey()
	row, err := s.st.CreateAPIKey(ctx, store.CreateAPIKeyInput{
		TenantID: tenantID,
		UserID:   userID,
		Name:     name,
		Prefix:   prefix,
		Hash:     hashAPIKey(plaintext),
	})
	if err != nil {
		return nil, "", err
	}
	return row, plaintext, nil
}

// ResolveAPIKey 校验明文 key，返回未撤销的 key 记录。
func (s *Service) ResolveAPIKey(ctx context.Context, plaintext string) (*store.APIKeyRow, error) {
	return s.st.GetAPIKeyByHash(ctx, hashAPIKey(plaintext))
}

func (s *Service) ListAPIKeys(ctx context.Context, tenantID string) ([]*store.APIKeyRow, error) {
	return s.st.ListAPIKeys(ctx, tenantID)
}

func (s *Service) RevokeAPIKey(ctx context.Context, id string) error {
	return s.st.RevokeAPIKey(ctx, id)
}

// ─── crypto helpers ─────────────────────────────────────────────────────────

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, pbkdf2KeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}

func (s *Service) signCookie(sessionID string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(sessionID))
	return sessionID + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) verifyCookie(value string) (string, bool) {
	i := strings.LastIndexByte(value, '.')
	if i <= 0 {
		return "", false
	}
	sessionID := value[:i]
	sig, err := base64.RawURLEncoding.DecodeString(value[i+1:])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(sessionID))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", false
	}
	return sessionID, true
}

// generateAPIKey 生成 `je_<id>_<secret>`：id 是公开标识（存 prefix 供列表识别），
// secret 是真正的随机部分。prefix 不含 secret，避免 ListAPIKeys 泄漏 key 材料。
func generateAPIKey() (plaintext, prefix string) {
	idBytes := make([]byte, 5)
	secretBytes := make([]byte, 20)
	_, _ = rand.Read(idBytes)
	_, _ = rand.Read(secretBytes)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	id := strings.ToLower(enc.EncodeToString(idBytes))         // 8 字符公开 id
	secret := strings.ToLower(enc.EncodeToString(secretBytes)) // 32 字符 secret（~160 bit）
	plaintext = "je_" + id + "_" + secret
	prefix = "je_" + id
	return plaintext, prefix
}

func hashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

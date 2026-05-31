package vault

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// session 识别 header（ADR-0006 V1 方案）：沙箱出站请求带上它，代理读取后剥离。
const sessionHeader = "X-Je-Session"

// 注入前剥离的客户端凭据 header（防 dummy 凭据外泄）。
var strippedHeaders = []string{"Authorization", "X-Api-Key", "Private-Token"}

// InjectFunc 给定 (sessionID, host) 返回应注入的 bearer token。
// 返回 ok=false 表示无匹配凭据，请求原样放行。
type InjectFunc func(ctx context.Context, sessionID, host string) (token string, ok bool)

// ─── CA ─────────────────────────────────────────────────────────────────────

// CA 是 MITM 用的自签名根证书。首次启动生成，落盘 data/je-vault-ca/。
type CA struct {
	Cert     *x509.Certificate
	Key      *ecdsa.PrivateKey
	CertPath string
}

// LoadOrCreateCA 从 dir 加载 CA；不存在则生成并写盘（key 0600）。
func LoadOrCreateCA(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	if certPEM, err := os.ReadFile(certPath); err == nil {
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read ca key: %w", err)
		}
		cb, _ := pem.Decode(certPEM)
		kb, _ := pem.Decode(keyPEM)
		if cb == nil || kb == nil {
			return nil, fmt.Errorf("malformed CA pem in %s", dir)
		}
		cert, err := x509.ParseCertificate(cb.Bytes)
		if err != nil {
			return nil, err
		}
		key, err := x509.ParseECPrivateKey(kb.Bytes)
		if err != nil {
			return nil, err
		}
		return &CA{Cert: cert, Key: key, CertPath: certPath}, nil
	}

	// 生成
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: "JadeEnvoy Vault CA", Organization: []string{"JadeEnvoy"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key, CertPath: certPath}, nil
}

func newSerial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	return n
}

// ─── Proxy ────────────────────────────────────────────────────────────────

// Proxy 是 HTTPS MITM 代理：拦截沙箱出站，按 host 匹配 vault 凭据注入 bearer。
// 纯 stdlib（crypto/tls + crypto/x509），不依赖第三方 proxy 库（见 ADR-0019）。
type Proxy struct {
	ca       *CA
	inject   InjectFunc
	upstream *http.Transport
	log      *slog.Logger

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// NewProxy 构造代理。upstreamTLS 用于代理→真实 server 的 TLS 校验（生产留 nil 用系统 CA；
// 测试可传 InsecureSkipVerify）。
func NewProxy(ca *CA, inject InjectFunc, upstreamTLS *tls.Config) *Proxy {
	if upstreamTLS == nil {
		upstreamTLS = &tls.Config{}
	}
	upstreamTLS.NextProtos = []string{"http/1.1"} // 简化：上游也走 HTTP/1.1
	return &Proxy{
		ca:     ca,
		inject: inject,
		log:    slog.Default(),
		leaves: map[string]*tls.Certificate{},
		upstream: &http.Transport{
			TLSClientConfig:   upstreamTLS,
			ForceAttemptHTTP2: false,
		},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handlePlain(w, r)
}

// handleConnect 对 CONNECT 做 MITM：终止客户端 TLS，逐请求注入后转发到真实 server。
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := hostnameOnly(r.Host)
	sessionID := sessionFromProxyAuth(r.Header.Get("Proxy-Authorization"))
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	tlsConn := tls.Server(clientConn, &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host
			}
			return p.leafFor(name)
		},
	})
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	defer tlsConn.Close()

	reader := bufio.NewReader(tlsConn)
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			return // EOF / 客户端关闭
		}
		req.URL.Scheme = "https"
		req.URL.Host = r.Host
		keepAlive := p.proxyOne(tlsConn, req, host, sessionID)
		if !keepAlive {
			return
		}
	}
}

// handlePlain 代理明文 HTTP（绝对 URL）。
func (p *Proxy) handlePlain(w http.ResponseWriter, r *http.Request) {
	if r.URL.Host == "" {
		http.Error(w, "non-proxy request", http.StatusBadRequest)
		return
	}
	host := hostnameOnly(r.URL.Host)
	sessionID := sessionFromProxyAuth(r.Header.Get("Proxy-Authorization"))
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	outReq.Header.Del("Proxy-Authorization")
	p.applyInjection(outReq, host, sessionID)
	resp, err := p.upstream.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// proxyOne 转发一个 MITM 解密出的请求，返回是否保持连接。
func (p *Proxy) proxyOne(clientConn io.Writer, req *http.Request, host, sessionID string) bool {
	req.RequestURI = ""
	p.applyInjection(req, host, sessionID)

	resp, err := p.upstream.RoundTrip(req)
	if err != nil {
		writeRawError(clientConn, err)
		return false
	}
	defer resp.Body.Close()
	if err := resp.Write(clientConn); err != nil {
		return false
	}
	return !req.Close && !resp.Close
}

// applyInjection 剥离客户端凭据，按 (session, host) 注入 vault bearer。
// sessionID 优先来自 CONNECT 的 Proxy-Authorization；为空时回退 X-Je-Session header。
func (p *Proxy) applyInjection(req *http.Request, host, sessionID string) {
	if sessionID == "" {
		sessionID = req.Header.Get(sessionHeader)
	}
	req.Header.Del(sessionHeader)

	token, ok := p.inject(req.Context(), sessionID, host)
	if !ok {
		return // 无匹配凭据 → 原样放行
	}
	for _, h := range strippedHeaders {
		req.Header.Del(h)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	p.log.Info("vault.inject", "session_id", sessionID, "host", host)
}

// leafFor 为 host 动态签发 leaf cert（由 CA 签名），缓存。
func (p *Proxy) leafFor(host string) (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.leaves[host]; ok {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca.Cert, &key.PublicKey, p.ca.Key)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{
		Certificate: [][]byte{der, p.ca.Cert.Raw},
		PrivateKey:  key,
	}
	p.leaves[host] = cert
	return cert, nil
}

// sessionFromProxyAuth 从 "Basic base64(session:...)" 解出 session id（取 username 段）。
// 沙箱通过 HTTPS_PROXY=http://<sessionID>:x@host:port 传递，免去改写每条 curl 命令。
func sessionFromProxyAuth(header string) string {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return ""
	}
	creds := string(raw)
	if i := strings.IndexByte(creds, ':'); i >= 0 {
		return creds[:i]
	}
	return creds
}

func hostnameOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func writeRawError(w io.Writer, err error) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": []string{"text/plain"}},
		Body:   io.NopCloser(strings.NewReader("vault proxy upstream error: " + err.Error())),
	}
	_ = resp.Write(w)
}

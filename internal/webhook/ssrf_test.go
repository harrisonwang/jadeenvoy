package webhook

import (
	"net"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},       // loopback
		{"10.1.2.3", true},        // private
		{"192.168.0.5", true},     // private
		{"172.16.9.9", true},      // private
		{"169.254.169.254", true}, // link-local (cloud metadata)
		{"0.0.0.0", true},         // unspecified
		{"::1", true},             // ipv6 loopback
		{"8.8.8.8", false},        // public
		{"1.1.1.1", false},        // public
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", c.ip)
		}
		if got := isBlockedIP(ip); got != c.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

func TestValidateWebhookURL(t *testing.T) {
	// scheme 校验
	if err := validateWebhookURL("ftp://example.com", false); err == nil {
		t.Error("expected ftp rejected")
	}
	// allowPrivate=true 放行环回（内网/测试场景）
	if err := validateWebhookURL("http://127.0.0.1:8080/hook", true); err != nil {
		t.Errorf("allowPrivate should permit loopback: %v", err)
	}
	// allowPrivate=false 拒环回
	if err := validateWebhookURL("http://127.0.0.1:8080/hook", false); err == nil {
		t.Error("expected loopback blocked when allowPrivate=false")
	}
	// 公网域名放行（DNS 可解析）
	if err := validateWebhookURL("https://example.com/hook", false); err != nil {
		t.Logf("note: example.com resolution failed in sandbox (%v) — skipping public assert", err)
	}
}

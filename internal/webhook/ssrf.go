package webhook

import (
	"fmt"
	"net"
	"net/url"
)

// validateWebhookURL 校验 webhook 目标 URL，防 SSRF。
//
// 默认拒绝环回 / 私网 / 链路本地 / 未指定 / 多播地址（attacker 用 webhook 探内网）。
// allowPrivate=true 时放行这些段 —— 内网部署场景（webhook 打内部服务）+ 测试。
//
// 注意：这是创建期的尽力校验；DNS 可能在投递时变化（TOCTOU），故投递期也会再查一次。
func validateWebhookURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url has no host")
	}
	if allowPrivate {
		return nil
	}
	// 解析 host → IP（字面 IP 直接解析；域名走 DNS）。
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("cannot resolve host %q: %w", host, err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("url resolves to a disallowed address (%s); set JE_WEBHOOK_ALLOW_PRIVATE=1 to permit internal targets", ip)
		}
	}
	return nil
}

// isBlockedIP 报告 ip 是否落在默认禁止段。
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// 云元数据地址（169.254.169.254 已被 LinkLocal 覆盖，这里冗余兜底）。
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return true
	}
	return false
}

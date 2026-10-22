package config

import (
	"net"
	"path/filepath"
	"strings"
)

// DefaultPublicListenPort 是公网 TLS 监听的默认端口。它与明文 HTTP 的 8787 分开，
// 因为公网入口必须始终是 HTTPS：明文跨公网会把 Token 直接暴露成可窃听的凭据。
const DefaultPublicListenPort = "8443"

// DefaultPublicTunnelPort 是隧道模式配对时公布的端口。隧道提供商（Cloudflare Tunnel
// 等）在边缘终结 TLS 并按 443 对外提供 https，本机只需把明文端口回源给它，
// 因此手机连的是 443 而不是 agentd 自己的 8443。
const DefaultPublicTunnelPort = "443"

// 公网 HTTPS 的 TLS 终结方式。两者互斥，决定 agentd 要不要自己监听公网 TLS 端口。
const (
	// PublicTLSTerminationSelfSigned 是默认值：agentd 自签证书直接对公网提供 HTTPS，
	// 手机靠配对二维码里的证书指纹建立信任。
	PublicTLSTerminationSelfSigned = "self_signed"
	// PublicTLSTerminationTunnel 是本机不监听公网端口、由隧道客户端把公网域名
	// 回源到本机明文端口。证书由隧道提供商的 CA 签发，手机按系统信任链校验。
	PublicTLSTerminationTunnel = "tunnel"
)

// PublicAccessConfig 是公网 IP 直连模块的本地配置。
//
// 它只描述「这台主机愿意在公网地址上提供 HTTPS 入口」，不包含任何凭据：
// 配对仍走一次性签名票据，传输信任由自签证书指纹提供。
type PublicAccessConfig struct {
	// nil 表示从未配置过公网模块；显式 false 才是「配过但关掉」。
	Enabled *bool `json:"enabled,omitempty"`
	// Listen 是公网 TLS 的绑定地址，例如 "0.0.0.0:8443"。为空时按默认端口绑定 IPv4 通配。
	Listen string `json:"listen,omitempty"`
	// Advertise 是配对时对外公布的地址（IP 或域名），可带端口。
	// 公网 IP 不会出现在本机网卡上（NAT、EIP），因此必须由用户显式写明；
	// 留空时只按网卡上真实可见的全局单播地址兜底。
	Advertise string `json:"advertise,omitempty"`
	// Cert/Key 允许改用用户自己的正式证书；为空时 agentd 在配置目录里自签一张。
	Cert string `json:"cert,omitempty"`
	Key  string `json:"key,omitempty"`
	// TLSTermination 决定 HTTPS 由谁终结：留空或 "self_signed" 时 agentd 自己监听
	// 公网 TLS 端口并出示自签证书；写 "tunnel" 时本机不监听任何公网端口，
	// 由隧道客户端（cloudflared 等）把公网域名回源到本机明文端口，证书归隧道提供商。
	TLSTermination string `json:"tls_termination,omitempty"`
}

// PublicAccessEnabled 报告公网直连是否开启。
func (c Config) PublicAccessEnabled() bool {
	return c.Network.Public != nil && c.Network.Public.Enabled != nil && *c.Network.Public.Enabled
}

// PublicListenAddress 返回公网 TLS 的绑定地址，缺省为 IPv4 通配 + 默认端口。
// 公网入口不能只绑 ::：多数手机仍在 IPv4 或 NAT64 上，IPv6 通配也不覆盖 IPv4。
func (c Config) PublicListenAddress() string {
	// 隧道模式下 agentd 不建公网监听：公网入口是隧道边缘，本机只保留明文端口回源。
	// 调用方（openPublicTLSListener）据此跳过 TLS 监听，无需各自判断模式。
	if !c.PublicServesOwnTLS() {
		return ""
	}
	address := strings.TrimSpace(c.publicRaw().Listen)
	if address == "" {
		return net.JoinHostPort("0.0.0.0", DefaultPublicListenPort)
	}
	if host, port, err := net.SplitHostPort(address); err == nil {
		if port == "" {
			return net.JoinHostPort(host, DefaultPublicListenPort)
		}
		return net.JoinHostPort(host, port)
	}
	if ip := net.ParseIP(strings.Trim(address, "[]")); ip != nil {
		return net.JoinHostPort(address, DefaultPublicListenPort)
	}
	return address
}

// PublicAdvertiseHost 返回配对要公布的公网主机名，去掉用户顺手写上的端口。
func (c Config) PublicAdvertiseHost() string {
	value := strings.TrimSpace(c.publicRaw().Advertise)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "[") {
		if host, _, err := net.SplitHostPort(strings.Trim(value, "[]")); err == nil {
			return host
		}
		return strings.Trim(value, "[]")
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

// PublicAdvertisePort 返回配对要公布的端口，优先取显式写出的端口。
func (c Config) PublicAdvertisePort() string {
	value := strings.TrimSpace(c.publicRaw().Advertise)
	if host, port, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimSuffix(value, "]"), "[")); err == nil && port != "" && host != "" {
		return port
	}
	if !c.PublicServesOwnTLS() {
		return DefaultPublicTunnelPort
	}
	_, port, err := net.SplitHostPort(c.PublicListenAddress())
	if err != nil {
		return DefaultPublicListenPort
	}
	return port
}

// PublicTLSTermination 返回公网 HTTPS 的终结方式。留空按自签处理以兼容既有配置；
// 写错的值也退回自签，避免拼写错误让公网入口静默消失。
func (c Config) PublicTLSTermination() string {
	if strings.EqualFold(strings.TrimSpace(c.publicRaw().TLSTermination), PublicTLSTerminationTunnel) {
		return PublicTLSTerminationTunnel
	}
	return PublicTLSTerminationSelfSigned
}

// PublicServesOwnTLS 报告 agentd 要不要自己监听公网 TLS 端口并出示本机证书。
// 隧道模式返回 false：证书归隧道提供商，本机既不监听也不该生成指纹。
func (c Config) PublicServesOwnTLS() bool {
	return c.PublicTLSTermination() != PublicTLSTerminationTunnel
}

// PublicCertificatePaths 返回证书与私钥路径。未配置自定义证书时落在配置目录内，
// 与 auth.token 同级，因此目录权限必须保持仅当前用户可访问。
func (c Config) PublicCertificatePaths(configPath string) (certPath string, keyPath string, custom bool) {
	raw := c.publicRaw()
	if strings.TrimSpace(raw.Cert) != "" || strings.TrimSpace(raw.Key) != "" {
		return strings.TrimSpace(raw.Cert), strings.TrimSpace(raw.Key), true
	}
	dir := filepath.Dir(strings.TrimSpace(configPath))
	if dir == "" || dir == "." {
		dir = "."
	}
	return filepath.Join(dir, "public-tls-server.crt"), filepath.Join(dir, "public-tls-server.key"), false
}

func (c Config) publicRaw() PublicAccessConfig {
	if c.Network.Public == nil {
		return PublicAccessConfig{}
	}
	return *c.Network.Public
}

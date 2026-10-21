package config

import (
	"net"
	"path/filepath"
	"strings"
)

// DefaultPublicListenPort 是公网 TLS 监听的默认端口。它与明文 HTTP 的 8787 分开，
// 因为公网入口必须始终是 HTTPS：明文跨公网会把 Token 直接暴露成可窃听的凭据。
const DefaultPublicListenPort = "8443"

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
}

// PublicAccessEnabled 报告公网直连是否开启。
func (c Config) PublicAccessEnabled() bool {
	return c.Network.Public != nil && c.Network.Public.Enabled != nil && *c.Network.Public.Enabled
}

// PublicListenAddress 返回公网 TLS 的绑定地址，缺省为 IPv4 通配 + 默认端口。
// 公网入口不能只绑 ::：多数手机仍在 IPv4 或 NAT64 上，IPv6 通配也不覆盖 IPv4。
func (c Config) PublicListenAddress() string {
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
	_, port, err := net.SplitHostPort(c.PublicListenAddress())
	if err != nil {
		return DefaultPublicListenPort
	}
	return port
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

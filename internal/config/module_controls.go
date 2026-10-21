package config

import (
	"net"
	"strings"
)

func (c CodexConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

func (c Config) HasEnabledAgent() bool { return c.Codex.IsEnabled() || c.Claude.Enabled }

// Independent ingress controls are opt-in, so upgrading does not change an
// existing custom bind. The first explicit network mutation records both intents.
func (c Config) HasNetworkModuleControls() bool { return c.Network.AllowTailscale != nil }

func (c Config) TailscaleAccessEnabled() bool {
	if c.Network.AllowTailscale != nil {
		return *c.Network.AllowTailscale
	}
	ip := c.configuredListenIP()
	return c.Network.AllowLAN || (ip != nil && (ip.IsUnspecified() || isTailscaleListenIP(ip)))
}

func (c Config) LANAccessEnabled() bool {
	if c.HasNetworkModuleControls() {
		return c.Network.AllowLAN
	}
	ip := c.configuredListenIP()
	return c.Network.AllowLAN || (ip != nil && (ip.IsUnspecified() || (ip.To4() != nil && ip.IsPrivate())))
}

func (c Config) configuredListenIP() net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(c.Listen))
	if err != nil {
		return nil
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

// In managed mode listen supplies the port, not an unrestricted bind. The
// listener is selected separately and every accepted socket is policy checked.
func (c Config) moduleValidationListen() string {
	if !c.HasNetworkModuleControls() {
		return c.Listen
	}
	_, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return c.Listen
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// ModuleStatus comes from the running daemon, never from the CLI's on-disk
// snapshot. Clients use it to confirm a reload actually applied a mutation.
type ModuleStatus struct {
	CodexEnabled       bool `json:"codex_enabled"`
	ClaudeEnabled      bool `json:"claude_enabled"`
	TailscaleEnabled   bool `json:"tailscale_enabled"`
	LANEnabled         bool `json:"lan_enabled"`
	TailscaleAvailable bool `json:"tailscale_available"`
	LANAvailable       bool `json:"lan_available"`
	// PublicEnabled 是「公网 IP 直连」模块的当前开关。它独立于 Tailscale 与局域网，
	// 开启后 agentd 额外监听一个 HTTPS 端口，公网入口永远不是明文 HTTP。
	PublicEnabled bool `json:"public_enabled"`
	// PublicAvailable 表示公网入口现在真的能给出配对地址（已 advertise 或网卡可见公网地址）。
	// 客户端据此区分「开关已开但地址还没配好」，不要把不可用的入口显示成可连接。
	PublicAvailable bool `json:"public_available"`
	// PublicEndpoint 是手机要连的公网 HTTPS 地址，未配置时为空。
	PublicEndpoint string `json:"public_endpoint,omitempty"`
	// PublicFingerprint 来自运行中的公网监听，不是磁盘快照。手机可用它核对
	// 配对时拿到的证书指纹是否仍然一致。
	PublicFingerprint string `json:"public_fingerprint,omitempty"`
}

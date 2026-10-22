package config

import "testing"

func publicConfig(public *PublicAccessConfig) Config {
	cfg := Config{Listen: "127.0.0.1:8787"}
	cfg.Network.Public = public
	return cfg
}

// TestPublicTLSTerminationDefaultsToSelfSigned 确认历史配置（没有该字段）
// 以及写错的值都退回自签，不会让已经在跑的公网入口静默消失。
func TestPublicTLSTerminationDefaultsToSelfSigned(t *testing.T) {
	for _, value := range []string{"", "SELF_SIGNED", "nonsense"} {
		cfg := publicConfig(&PublicAccessConfig{Enabled: boolPtr(true), TLSTermination: value})
		if cfg.PublicTLSTermination() != PublicTLSTerminationSelfSigned || !cfg.PublicServesOwnTLS() {
			t.Fatalf("终结方式 %q 应退回自签：%s", value, cfg.PublicTLSTermination())
		}
		if got := cfg.PublicListenAddress(); got != "0.0.0.0:"+DefaultPublicListenPort {
			t.Fatalf("自签模式默认监听异常：%q", got)
		}
		if got := cfg.PublicAdvertisePort(); got != DefaultPublicListenPort {
			t.Fatalf("自签模式默认公布端口异常：%s", got)
		}
	}
}

// TestPublicTLSTerminationTunnelDropsOwnListener 确认隧道模式下本机不建公网监听，
// 配对公布的是隧道边缘的 443，而不是 agentd 自己的 8443。
func TestPublicTLSTerminationTunnelDropsOwnListener(t *testing.T) {
	cfg := publicConfig(&PublicAccessConfig{
		Enabled: boolPtr(true), Advertise: "mimi.example.com", TLSTermination: PublicTLSTerminationTunnel,
	})
	if cfg.PublicTLSTermination() != PublicTLSTerminationTunnel || cfg.PublicServesOwnTLS() {
		t.Fatal("隧道模式应报告不由本机终结 TLS")
	}
	if got := cfg.PublicListenAddress(); got != "" {
		t.Fatalf("隧道模式本机不应监听公网端口：%q", got)
	}
	if got := cfg.PublicAdvertisePort(); got != DefaultPublicTunnelPort {
		t.Fatalf("隧道模式默认公布端口应为 443：%s", got)
	}
	if got := cfg.PublicAdvertiseHost(); got != "mimi.example.com" {
		t.Fatalf("隧道域名解析异常：%s", got)
	}
	// 显式写端口时仍以用户写的为准，便于隧道挂在非标准端口上。
	custom := publicConfig(&PublicAccessConfig{
		Enabled: boolPtr(true), Advertise: "mimi.example.com:8443", TLSTermination: PublicTLSTerminationTunnel,
	})
	if got := custom.PublicAdvertisePort(); got != "8443" {
		t.Fatalf("advertise 自带端口应生效：%s", got)
	}
}

func boolPtr(value bool) *bool { return &value }

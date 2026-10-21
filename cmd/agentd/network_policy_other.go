//go:build !windows

package main

func ensurePlatformLANAccessAllowed() error {
	return nil
}

// ensurePlatformPublicAccessAllowed 在非 Windows 上没有系统级入站规则需要维护。
// 路由器、安全组和云厂商弹性 IP 的放行不在 agentd 的控制范围内，
// 因此公网状态只报告本机意图，不声称端到端可达。
func ensurePlatformPublicAccessAllowed() error {
	return nil
}

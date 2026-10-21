package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/gaixianggeng/mimi-remote/internal/config"
	"github.com/gaixianggeng/mimi-remote/internal/directtls"
	"github.com/gaixianggeng/mimi-remote/internal/networkaccess"
	agentsetup "github.com/gaixianggeng/mimi-remote/internal/setup"
)

// publicTLSListener 是公网 HTTPS 入口的运行态：已绑定的监听、可用证书和指纹。
type publicTLSListener struct {
	Listener    net.Listener
	Material    directtls.Material
	Fingerprint string
}

// openPublicTLSListener 按配置准备公网监听。证书要么用用户自备的正式证书，
// 要么在配置目录里自签一张把公网地址写进 SAN 的证书；指纹随后注入 Router，
// 让 /api/pair/claim 能把信任依据回给已经扫过码的手机。
func openPublicTLSListener(cfg config.Config, configPath string) (publicTLSListener, error) {
	address := publicListenAddress(cfg)
	if address == "" {
		return publicTLSListener{}, nil
	}
	material, err := agentsetup.PublicTLSMaterial(configPath, cfg)
	if err != nil {
		return publicTLSListener{}, fmt.Errorf("准备公网 TLS 证书失败：%w", err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return publicTLSListener{}, fmt.Errorf("监听公网地址 %s 失败：%w", address, err)
	}
	return publicTLSListener{
		Listener:    networkaccess.Wrap(listener, publicIngressPolicy()),
		Material:    material,
		Fingerprint: material.Fingerprint,
	}, nil
}

// servePublicTLSListener 在独立 goroutine 上用同一份 handler 提供 HTTPS。
// 明文监听继续走 server.Serve，两者共用 http.Server 但互不影响：
// Serve 不读 TLSConfig，只有 ServeTLS 才使用它。
func servePublicTLSListener(server *http.Server, public publicTLSListener, errCh chan error) {
	if public.Listener == nil {
		return
	}
	server.TLSConfig = public.Material.TLSConfig()
	go func() {
		fingerprint := strings.TrimSpace(public.Fingerprint)
		if fingerprint != "" {
			log.Printf("agentd listening on https://%s（公网证书指纹 %s）", public.Listener.Addr(), fingerprint)
		} else {
			log.Printf("agentd listening on https://%s", public.Listener.Addr())
		}
		errCh <- server.ServeTLS(public.Listener, "", "")
	}()
}

// publicTLSFailureIsFatal 决定公网证书准备失败要不要终止启动。
// 只有公网时降级会让 agentd 看似在跑但手机永远连不上，因此必须 fail-fast；
// 仍有 Tailscale 或局域网入口时降级为警告，本机和其它通道继续可用。
func publicTLSFailureIsFatal(cfg config.Config) bool {
	return !cfg.TailscaleAccessEnabled() && !cfg.LANAccessEnabled()
}

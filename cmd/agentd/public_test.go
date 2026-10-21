package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"runtime"
	"strings"
	"testing"

	"github.com/gaixianggeng/mimi-remote/internal/config"
	"github.com/gaixianggeng/mimi-remote/internal/networkaccess"
	agentsetup "github.com/gaixianggeng/mimi-remote/internal/setup"
)

// publicInternetPeer 是手机从公网进来的 socket：本地地址是主机自己的公网 IP，
// 来源地址既不是私网也不是 tailnet，无法预先枚举。
var publicInternetPeer = [2]string{"203.0.113.9", "198.51.100.4"}

func publicTestConfigPath(t *testing.T, network string) string {
	t.Helper()
	return writeNetworkToggleConfig(t, "127.0.0.1:8787", network)
}

func runPublicJSON(t *testing.T, path string, args ...string) agentsetup.PublicAccessConfigurationResult {
	t.Helper()
	var stdout bytes.Buffer
	cmd := append([]string{"public", "--config", path, "--json"}, args...)
	if err := runPublicWithWriters(cmd, &stdout, io.Discard); err != nil {
		t.Fatalf("agentd %s: %v", strings.Join(cmd, " "), err)
	}
	var result agentsetup.PublicAccessConfigurationResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode public result: %v\n%s", err, stdout.Bytes())
	}
	return result
}

func TestPublicCLIAdvertiseEnablesHTTPSEntrypoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 安装器只放行 Private/LocalSubnet 来源，公网直连在 CLI 处 fail-fast")
	}
	path := publicTestConfigPath(t, `{"allow_lan":false,"allow_tailscale":false}`)
	result := runPublicJSON(t, path, "--enabled=true", "--advertise=203.0.113.9")
	if !result.Enabled || !result.RestartRequired {
		t.Fatalf("开启公网后应要求重启：%+v", result)
	}
	if result.Endpoint != "https://203.0.113.9:8443" {
		t.Fatalf("公网入口必须是 HTTPS：%s", result.Endpoint)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PublicAccessEnabled() || cfg.Listen != "127.0.0.1:8787" {
		t.Fatalf("公网开关改动了明文监听语义：%+v", cfg)
	}
	if cfg.Auth.Token != "network-toggle-test-only" {
		t.Fatal("公网开关丢失了 auth.token")
	}

	off := runPublicJSON(t, path, "--enabled=false")
	if off.Enabled || off.Advertise != "203.0.113.9" {
		t.Fatalf("关闭公网后应保留已配好的地址：%+v", off)
	}
}

func TestPublicCLIRejectsTwoModesAtOnce(t *testing.T) {
	path := publicTestConfigPath(t, `{"allow_lan":false,"allow_tailscale":false}`)
	var stdout, stderr bytes.Buffer
	if err := runPublicWithWriters([]string{"public", "--config", path, "--enabled=true", "--status", "--json"}, &stdout, &stderr); err == nil {
		t.Fatal("--enabled 与 --status 同时给出时必须报错")
	}
}

func TestPublicListenerStaysSeparateFromPlaintextIngress(t *testing.T) {
	path := publicTestConfigPath(t, `{"allow_lan":false,"allow_tailscale":false}`)
	if err := runPublicWithWriters([]string{"public", "--config", path, "--enabled=true", "--advertise=203.0.113.9", "--json"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// 公网开启不能让明文监听变成通配：Token 跨公网必须是 TLS。
	for _, address := range moduleListenAddresses(cfg) {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			t.Fatal(splitErr)
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			t.Fatalf("公网开启后明文监听扩大到非 loopback：%v", moduleListenAddresses(cfg))
		}
	}
	if got := publicListenAddress(cfg); got != "0.0.0.0:8443" {
		t.Fatalf("公网 TLS 监听地址异常：%s", got)
	}
	if moduleIngressPolicy(cfg).Allows(net.ParseIP(publicInternetPeer[0]), net.ParseIP(publicInternetPeer[1])) {
		t.Fatal("明文监听不得因公网模块开启而放行公网来源")
	}
	policy := publicIngressPolicy()
	if !policy.Allows(net.ParseIP(publicInternetPeer[0]), net.ParseIP(publicInternetPeer[1])) {
		t.Fatal("公网 TLS 监听必须放行任意来源")
	}
	if !policy.Allows(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1")) {
		t.Fatal("公网 TLS 监听必须保留本机 loopback")
	}
	if (networkaccess.Policy{}).Allows(net.ParseIP(publicInternetPeer[0]), net.ParseIP(publicInternetPeer[1])) {
		t.Fatal("公网模块关闭时公网来源必须被拒")
	}
	if got := publicListenAddress(mustLoadConfigWithoutPublic(t, path)); got != "" {
		t.Fatalf("公网关闭时不应建立公网监听：%s", got)
	}
}

func TestPublicTLSFailureIsFatalOnlyWhenItIsTheOnlyIngress(t *testing.T) {
	onlyPublic := config.Config{Network: config.NetworkConfig{
		Public:         &config.PublicAccessConfig{Enabled: pointer(true)},
		AllowTailscale: pointer(false),
	}}
	if !publicTLSFailureIsFatal(onlyPublic) {
		t.Fatal("只有公网入口时证书失败必须让启动失败，否则手机连不上却看不到原因")
	}
	withLAN := config.Config{Network: config.NetworkConfig{
		Public:         &config.PublicAccessConfig{Enabled: pointer(true)},
		AllowLAN:       true,
		AllowTailscale: pointer(false),
	}}
	if publicTLSFailureIsFatal(withLAN) {
		t.Fatal("仍有局域网入口时证书失败应降级为警告，本机与其它通道继续可用")
	}
}

func mustLoadConfigWithoutPublic(t *testing.T, path string) config.Config {
	t.Helper()
	result := runPublicJSON(t, path, "--enabled=false")
	if result.Enabled {
		t.Fatal("公网未关闭")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func pointer(value bool) *bool { return &value }

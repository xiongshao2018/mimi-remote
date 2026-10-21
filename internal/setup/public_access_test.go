package setup

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gaixianggeng/mimi-remote/internal/auth"
	"github.com/gaixianggeng/mimi-remote/internal/config"
)

// publicTestConfig 复用模块测试的配置骨架，但把 listen 换成本机 loopback，
// 避免公网用例被 Tailscale 监听语义干扰。
func publicTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	document := map[string]any{
		"listen":      "127.0.0.1:8787",
		"auth":        map[string]any{"token": "public-test-only-token"},
		"app_server":  map[string]any{"transport": "local"},
		"codex":       map[string]any{"bin": "/missing/public-test-codex"},
		"claude":      map[string]any{"enabled": false},
		"network":     map[string]any{"allow_lan": false, "future": 17},
		"future_root": []string{"keep"},
		"projects":    []config.ProjectConfig{{ID: "public-test", Name: "Public test", Path: t.TempDir()}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPublicAccessEnableKeepsOtherModulesAndRemembersAddress(t *testing.T) {
	path := publicTestConfig(t)
	result, err := ConfigurePublicAccess(path, true, "", "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.RestartRequired || !result.Enabled {
		t.Fatalf("首次开启应要求重启：%+v", result)
	}
	if result.Previous != nil {
		t.Fatalf("从未配置过公网时不应给出 Previous：%+v", result.Previous)
	}
	if result.Endpoint != "https://203.0.113.9:8443" {
		t.Fatalf("公网配对地址应为 HTTPS 且使用默认端口：%s", result.Endpoint)
	}
	cfg := moduleConfig(t, path)
	if !cfg.PublicAccessEnabled() {
		t.Fatal("network.public.enabled 未写入")
	}
	if got := cfg.PublicListenAddress(); got != "0.0.0.0:8443" {
		t.Fatalf("公网默认绑定地址异常：%s", got)
	}
	if cfg.PublicAdvertiseHost() != "203.0.113.9" || cfg.PublicAdvertisePort() != "8443" {
		t.Fatalf("公网地址解析异常：%s:%s", cfg.PublicAdvertiseHost(), cfg.PublicAdvertisePort())
	}
	if cfg.Listen != "127.0.0.1:8787" || cfg.Auth.Token != "public-test-only-token" || cfg.LANAccessEnabled() {
		t.Fatal("公网开关改动了无关配置")
	}
	assertPublicUnknowns(t, path)

	// 关闭时只改 enabled，公网地址仍留着供下次开启复用。
	off, err := ConfigurePublicAccess(path, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if off.Enabled || off.Advertise != "203.0.113.9" || off.Endpoint != "" {
		t.Fatalf("关闭公网后状态异常：%+v", off)
	}
	if moduleConfig(t, path).PublicAccessEnabled() {
		t.Fatal("关闭公网后仍报告开启")
	}
}

func TestPublicAccessRestoreNeverConfiguredRemovesSection(t *testing.T) {
	path := publicTestConfig(t)
	result, err := ConfigurePublicAccess(path, true, "0.0.0.0:9443", "203.0.113.9:9443")
	if err != nil {
		t.Fatal(err)
	}
	if got := moduleConfig(t, path).PublicAdvertisePort(); got != "9443" {
		t.Fatalf("advertise 自带端口应生效：%s", got)
	}
	restored, err := RestorePublicAccess(path, result)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Enabled {
		t.Fatalf("还原到从未配置状态应关闭公网：%+v", restored)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	network, ok := document["network"].(map[string]any)
	if !ok {
		t.Fatalf("network 段丢失：%s", raw)
	}
	if _, exists := network["public"]; exists {
		t.Fatalf("从未配置过的公网不应留下 network.public 键：%s", raw)
	}
	if network["future"] != float64(17) {
		t.Fatal("还原公网时改动了无关字段")
	}
}

func TestPublicAccessRestoreRejectsNewerIntent(t *testing.T) {
	path := publicTestConfig(t)
	result, err := ConfigurePublicAccess(path, true, "", "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigurePublicAccess(path, true, "", "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	if _, err := RestorePublicAccess(path, result); err == nil {
		t.Fatal("公网地址已被别人改过，还原必须拒绝覆盖")
	}
	if got := moduleConfig(t, path).PublicAdvertiseHost(); got != "198.51.100.7" {
		t.Fatalf("被拒绝的还原改动了新设置：%s", got)
	}
}

func TestPublicPairingEndpointRequiresAdvertiseWithoutVisiblePublicNIC(t *testing.T) {
	cfg := config.Config{Network: config.NetworkConfig{
		Public: &config.PublicAccessConfig{Enabled: boolPointer(true)},
	}}
	lookups := pairingNetworkLookups{publicIP: func() string { return "" }}
	endpoint, _, err := publicPairingEndpoint(cfg, lookups)
	if err == nil {
		t.Fatalf("NAT 后没有公网网卡时必须报错，不能给出空地址：%s", endpoint)
	}
	if !strings.Contains(err.Error(), "--advertise") {
		t.Fatalf("报错必须给出可执行命令：%v", err)
	}
}

func TestPublicPairingEndpointPrefersAdvertiseOverNIC(t *testing.T) {
	cfg := config.Config{Network: config.NetworkConfig{
		Public: &config.PublicAccessConfig{Enabled: boolPointer(true), Advertise: "203.0.113.9"},
	}}
	lookups := pairingNetworkLookups{publicIP: func() string { return "198.51.100.7" }}
	endpoint, _, err := publicPairingEndpoint(cfg, lookups)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://203.0.113.9:8443" {
		t.Fatalf("弹性公网 IP 必须优先于网卡地址：%s", endpoint)
	}
}

func TestPublicPairingURLPinIsSignedAndFailClosed(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	issued := time.Now().UTC().Add(-time.Second)
	raw := pairingURL("https://203.0.113.9:8443", secret, "ZmluZ2VycHJpbnQ=", issued, issued.Add(defaultPairingURLTTL))
	if !strings.Contains(raw, "tls_pin=") {
		t.Fatalf("公网二维码必须带出证书指纹：%s", raw)
	}
	ticket := pairingTicketFromURL(t, raw)
	if ticket.TLSPin != "ZmluZ2VycHJpbnQ=" {
		t.Fatalf("二维码指纹解析异常：%+v", ticket)
	}
	now := time.Now().UTC()
	if err := auth.ValidatePairingTicket(secret, ticket, now); err != nil {
		t.Fatalf("带指纹票据应验签通过：%v", err)
	}
	swapped := ticket
	swapped.TLSPin = "ZmluZ2VycHJpbnU="
	if err := auth.ValidatePairingTicket(secret, swapped, now); err == nil {
		t.Fatal("中间人替换指纹后必须验签失败")
	}
	dropped := ticket
	dropped.TLSPin = ""
	if err := auth.ValidatePairingTicket(secret, dropped, now); err == nil {
		t.Fatal("中间人抹掉指纹后不能退回旧签名")
	}
}

func TestPublicTLSMaterialSANCoversAdvertiseAndStaysStable(t *testing.T) {
	path := publicTestConfig(t)
	if _, err := ConfigurePublicAccess(path, true, "", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	cfg := moduleConfig(t, path)
	material, err := PublicTLSMaterial(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if material.Reused {
		t.Fatal("首次准备证书不应报告复用")
	}
	if material.Certificate.Leaf == nil {
		t.Fatal("证书缺少 Leaf")
	}
	found := false
	for _, ip := range material.Certificate.Leaf.IPAddresses {
		if ip.Equal(net.ParseIP("203.0.113.9")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("公网 IP 必须写进证书 SAN，否则 iOS 在指纹之前就拒绝握手：%v", material.Certificate.Leaf.IPAddresses)
	}
	if material.Fingerprint == "" {
		t.Fatal("证书指纹不能为空")
	}
	if info, err := os.Stat(material.KeyPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("私钥权限过宽：%v", info.Mode())
	}
	second, err := PublicTLSMaterial(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || second.Fingerprint != material.Fingerprint {
		t.Fatalf("重复准备必须复用同一张证书，否则每次重启都要重新配对：%+v", second)
	}

	// 公网 IP 变了必须重签：旧指纹对新地址毫无意义。
	if _, err := ConfigurePublicAccess(path, true, "", "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	changed, err := PublicTLSMaterial(path, moduleConfig(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == material.Fingerprint {
		t.Fatal("公网地址变化后证书指纹必须变化")
	}
}

func TestPublicAccessStatusReportsDisabledWithoutTouchingDisk(t *testing.T) {
	path := publicTestConfig(t)
	state, err := PublicAccessStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Enabled || state.Endpoint != "" || state.Fingerprint != "" {
		t.Fatalf("未配置公网时状态应为空：%+v", state)
	}
	certPath, _, _ := moduleConfig(t, path).PublicCertificatePaths(path)
	if _, err := os.Stat(certPath); !os.IsNotExist(err) {
		t.Fatal("状态查询不能生成证书")
	}
}

// assertPublicUnknowns 确认公网事务只改 network.public，其它模块和未知字段原样保留。
func assertPublicUnknowns(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	authSection, ok := document["auth"].(map[string]any)
	if !ok || authSection["token"] != "public-test-only-token" {
		t.Fatalf("auth 段被改动：%s", raw)
	}
	network, ok := document["network"].(map[string]any)
	if !ok {
		t.Fatalf("network 段丢失：%s", raw)
	}
	if network["future"] != float64(17) || network["allow_lan"] != false {
		t.Fatalf("network 段的无关字段被改动：%s", raw)
	}
	if document["future_root"] == nil {
		t.Fatalf("根级未知字段丢失：%s", raw)
	}
	if _, ok := document["codex"].(map[string]any); !ok {
		t.Fatalf("codex 段丢失：%s", raw)
	}
}

func boolPointer(value bool) *bool { return &value }

package setup

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/gaixianggeng/mimi-remote/internal/config"
	"github.com/gaixianggeng/mimi-remote/internal/directtls"
)

// publicPairingEndpoint 返回公网直连的配对地址。它只决定「手机该连哪里」，
// 证书与指纹由 publicTLSMaterial 单独负责，这样 agentd 未启动时也能先出二维码。
func publicPairingEndpoint(cfg config.Config, lookups pairingNetworkLookups) (string, []string, error) {
	host := strings.TrimSpace(cfg.PublicAdvertiseHost())
	source := "network.public.advertise"
	if host == "" {
		host = strings.TrimSpace(lookups.publicIP())
		source = "本机网卡"
	}
	if host == "" {
		return "", nil, fmt.Errorf(
			"未检测到公网地址：请用 agentd public --advertise <公网IP或域名> 写明手机要连的地址（NAT 或 EIP 不会出现在本机网卡上）",
		)
	}
	warnings := []string{
		fmt.Sprintf("公网配对地址来自%s，手机需能直达 https://%s:%s", source, host, cfg.PublicAdvertisePort()),
	}
	if net.ParseIP(host) == nil && !strings.Contains(host, ".") {
		warnings = append(warnings, "公网配对地址不是 IP 也不是完整域名，手机可能无法解析")
	}
	if !cfg.PublicServesOwnTLS() {
		// 隧道模式下证书由隧道提供商的 CA 签发，手机走系统信任链，
		// 二维码里不带指纹；带指纹反而会把合法的边缘证书判成中间人。
		warnings = append(warnings, "TLS 由隧道终结：证书由隧道提供商签发，手机按系统信任链校验，需把隧道域名回源到本机明文端口")
	}
	return httpsEndpoint(host, cfg.PublicAdvertisePort()), warnings, nil
}

// publicTLSMaterial 准备公网监听的证书。用户给了正式证书就直接用；
// 否则在配置目录里自签一张把公网地址写进 SAN 的证书。
func publicTLSMaterial(configPath string, cfg config.Config, lookups pairingNetworkLookups) (directtls.Material, error) {
	certPath, keyPath, custom := cfg.PublicCertificatePaths(configPath)
	if custom {
		if strings.TrimSpace(certPath) == "" || strings.TrimSpace(keyPath) == "" {
			return directtls.Material{}, fmt.Errorf("network.public 使用自定义证书时必须同时提供 cert 与 key")
		}
		material, err := directtls.Load(certPath, keyPath)
		if err != nil {
			return directtls.Material{}, fmt.Errorf("加载 network.public 指定的证书失败：%w", err)
		}
		return material, nil
	}
	hosts := publicCertificateHosts(cfg, lookups)
	return directtls.Ensure(certPath, hosts)
}

// publicCertificateHosts 决定 SAN 内容。手机按 IP 连接时证书必须含该 IP，
// 否则 iOS 在指纹命中之前就会因名称不匹配拒绝握手。
func publicCertificateHosts(cfg config.Config, lookups pairingNetworkLookups) []string {
	hosts := []string{"localhost"}
	if host := strings.TrimSpace(cfg.PublicAdvertiseHost()); host != "" {
		hosts = append(hosts, host)
	} else if host := strings.TrimSpace(lookups.publicIP()); host != "" {
		hosts = append(hosts, host)
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(cfg.PublicListenAddress())); err == nil {
		if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// PublicTLSMaterial 是 serve 侧的公开入口：公网监听启动时准备证书。
// 与配对路径共用同一份 SAN 推导，保证二维码里的指纹和监听实际出示的证书一致。
func PublicTLSMaterial(configPath string, cfg config.Config) (directtls.Material, error) {
	return publicTLSMaterial(configPath, cfg, defaultPairingNetworkLookups())
}

// publicTLSPin 是配对二维码里的信任依据。证书还没生成时（agentd 首次启动前）
// 这里会顺手生成，保证 setup/pair 打印的二维码从一开始就带指纹。
func publicTLSPin(configPath string, cfg config.Config, lookups pairingNetworkLookups) (string, error) {
	if !cfg.PublicServesOwnTLS() {
		// 隧道模式没有本机证书可指纹化：留空让手机退回系统信任链，
		// 而不是拿一张根本没在公网上出示的自签证书指纹去 fail-closed。
		return "", nil
	}
	material, err := publicTLSMaterial(configPath, cfg, lookups)
	if err != nil {
		return "", err
	}
	return material.Fingerprint, nil
}

func httpsEndpoint(host string, port string) string {
	return (&url.URL{Scheme: "https", Host: net.JoinHostPort(host, port)}).String()
}

// firstPublicIPv4 返回网卡上真实可见的全局单播 IPv4。它只在用户没有显式
// advertise 时兜底：NAT 后的主机通常一个都没有，此时调用方会给出可执行的报错。
func firstPublicIPv4() string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil || v4.IsLoopback() || v4.IsPrivate() || v4.IsLinkLocalUnicast() || isTailscaleIPv4(v4) {
			continue
		}
		return v4.String()
	}
	return ""
}

// PublicAccessState 是公网模块的当前意图，供 CLI 与安装器回显。
type PublicAccessState struct {
	Enabled     bool   `json:"enabled"`
	Listen      string `json:"listen"`
	Advertise   string `json:"advertise"`
	Endpoint    string `json:"endpoint,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// TLSTermination 回显 HTTPS 由谁终结，self_signed 或 tunnel。
	TLSTermination string `json:"tls_termination,omitempty"`
}

// PublicAccessStatus 读取当前配置里的公网入口，不生成也不改动证书。
func PublicAccessStatus(configPath string) (PublicAccessState, error) {
	cfgPath, err := resolveConfigPath(configPath)
	if err != nil {
		return PublicAccessState{}, err
	}
	cfg, err := config.LoadForDoctor(cfgPath)
	if err != nil {
		return PublicAccessState{}, err
	}
	return publicAccessState(cfgPath, cfg, defaultPairingNetworkLookups()), nil
}

// PublicAccessRuntimeStatus 用运行中的配置快照推导公网入口状态，不重新读磁盘。
// /api/host/modules 必须报告 agentd 实际在跑的意图，不能返回 CLI 刚写下的另一份配置。
func PublicAccessRuntimeStatus(configPath string, cfg config.Config) PublicAccessState {
	return publicAccessState(configPath, cfg, defaultPairingNetworkLookups())
}

func publicAccessState(configPath string, cfg config.Config, lookups pairingNetworkLookups) PublicAccessState {
	state := PublicAccessState{
		Enabled:        cfg.PublicAccessEnabled(),
		Listen:         cfg.PublicListenAddress(),
		Advertise:      cfg.PublicAdvertiseHost(),
		TLSTermination: cfg.PublicTLSTermination(),
	}
	if !state.Enabled {
		return state
	}
	endpoint, _, err := publicPairingEndpoint(cfg, lookups)
	if err != nil {
		return state
	}
	state.Endpoint = endpoint
	if !cfg.PublicServesOwnTLS() {
		// 隧道模式没有本机指纹，状态到此为止就是完整的。
		return state
	}
	// 状态查询不生成证书：agentd 还没起过时这里就是空指纹，二维码会在 pair 时按需生成。
	certPath, keyPath, _ := cfg.PublicCertificatePaths(configPath)
	if _, err := os.Stat(certPath); err != nil {
		return state
	}
	if material, err := directtls.Load(certPath, keyPath); err == nil {
		state.Fingerprint = material.Fingerprint
	}
	return state
}

// PublicAccessConfigurationResult 是一次公网开关事务的完整回执，Restore 需要它里面的
// Previous 才能把配置精确还原，不会覆盖别人在此期间改过的其它字段。
type PublicAccessConfigurationResult struct {
	Enabled         bool               `json:"enabled"`
	Listen          string             `json:"listen"`
	Advertise       string             `json:"advertise"`
	TLSTermination  string             `json:"tls_termination,omitempty"`
	Endpoint        string             `json:"endpoint,omitempty"`
	Fingerprint     string             `json:"fingerprint,omitempty"`
	Changed         bool               `json:"changed"`
	RestartRequired bool               `json:"restart_required"`
	Previous        *PublicAccessState `json:"previous,omitempty"`
	Applied         PublicAccessState  `json:"applied"`
}

// ConfigurePublicAccess 只改 network.public，保留 token、listen、配对和其它未知字段。
// 关闭公网时仍记住 listen/advertise，用户下次打开不必重填。
// tlsTermination 传 "" 表示沿用当前配置里的终结方式，避免只改 advertise 时把
// tunnel 悄悄改回自签；显式写 self_signed 才是真的切回本机监听。
func ConfigurePublicAccess(path string, enabled bool, listen, advertise, tlsTermination string) (PublicAccessConfigurationResult, error) {
	doc, err := readModuleDocument(path, "network")
	if err != nil {
		return PublicAccessConfigurationResult{}, err
	}
	before := publicAccessState(doc.path, doc.cfg, defaultPairingNetworkLookups())
	// 「从未配置过」只能看原始 JSON：publicAccessState 会把缺省值补成 0.0.0.0:8443，
	// 用它判断会让首次开启也带上 Previous，Restore 就退化成写 enabled=false。
	neverConfigured := doc.cfg.Network.Public == nil
	next := config.PublicAccessConfig{
		Enabled:        &enabled,
		Listen:         strings.TrimSpace(listen),
		Advertise:      strings.TrimSpace(advertise),
		TLSTermination: strings.TrimSpace(tlsTermination),
	}
	// 关闭公网时也记住 listen/advertise：下次重新开启不必重填，
	// 也避免「只是临时关掉」把用户配好的公网地址一起丢掉。
	if next.Advertise == "" {
		next.Advertise = before.Advertise
	}
	if next.Listen == "" {
		next.Listen = before.Listen
	}
	if next.TLSTermination == "" {
		next.TLSTermination = before.TLSTermination
	}
	payload := map[string]any{"enabled": enabled}
	if next.Listen != "" {
		payload["listen"] = next.Listen
	}
	if next.Advertise != "" {
		payload["advertise"] = next.Advertise
	}
	if next.TLSTermination != "" && next.TLSTermination != config.PublicTLSTerminationSelfSigned {
		payload["tls_termination"] = next.TLSTermination
	}
	after := publicAccessStateFrom(doc.cfg, next)
	changed := before.Enabled != after.Enabled || before.Listen != after.Listen ||
		before.Advertise != after.Advertise || before.TLSTermination != after.TLSTermination
	if changed {
		if err := doc.commit("network", map[string]any{"public": payload}); err != nil {
			return PublicAccessConfigurationResult{}, err
		}
	}
	result := PublicAccessConfigurationResult{
		Enabled: after.Enabled, Listen: after.Listen, Advertise: after.Advertise,
		TLSTermination: after.TLSTermination,
		Endpoint:       after.Endpoint, Fingerprint: after.Fingerprint,
		Changed: changed, RestartRequired: changed,
		Applied: after,
	}
	if !neverConfigured {
		previous := before
		result.Previous = &previous
	}
	return result, nil
}

// RestorePublicAccess 是 compare-and-swap：当前公网意图必须仍是 Applied，才写回 Previous。
func RestorePublicAccess(path string, previous PublicAccessConfigurationResult) (PublicAccessConfigurationResult, error) {
	doc, err := readModuleDocument(path, "network")
	if err != nil {
		return PublicAccessConfigurationResult{}, err
	}
	current := publicAccessState(doc.path, doc.cfg, defaultPairingNetworkLookups())
	if !samePublicAccessState(current, previous.Applied) {
		return PublicAccessConfigurationResult{}, fmt.Errorf("公网连接设置已被其他操作修改，未覆盖新的设置；请刷新后重试")
	}
	if previous.Previous == nil {
		if current.Enabled {
			if err := doc.commit("network", map[string]any{"public": nil}); err != nil {
				return PublicAccessConfigurationResult{}, err
			}
		}
		return PublicAccessConfigurationResult{
			Enabled: false, Listen: config.Config{}.PublicListenAddress(),
			Changed: current.Enabled, RestartRequired: current.Enabled, Applied: PublicAccessState{},
		}, nil
	}
	return ConfigurePublicAccess(path, previous.Previous.Enabled, previous.Previous.Listen,
		previous.Previous.Advertise, previous.Previous.TLSTermination)
}

func publicAccessStateFrom(cfg config.Config, public config.PublicAccessConfig) PublicAccessState {
	clone := cfg
	clone.Network.Public = &public
	return publicAccessState("", clone, defaultPairingNetworkLookups())
}

func samePublicAccessState(left, right PublicAccessState) bool {
	return left.Enabled == right.Enabled && left.Listen == right.Listen &&
		left.Advertise == right.Advertise && left.TLSTermination == right.TLSTermination
}

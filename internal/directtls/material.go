// Package directtls 为「公网 IP 直连」提供自签 TLS 证书与证书指纹。
//
// 手机在没有 Tailscale 的公网里只能按 IP 连上 agentd，系统信任链里没有这张证书，
// 所以证书必须自签、必须把公网 IP/域名写进 SAN，信任依据改由配对时带外下发的
// 证书指纹提供（iOS 端做 certificate pinning）。
package directtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// CertificateFileName 与 PrivateKeyFileName 落在 agentd 配置目录下，只服务公网监听。
	// 本机 loopback 与 Tailscale/局域网监听继续走明文 HTTP，不受这里影响。
	CertificateFileName = "public-tls-server.crt"
	PrivateKeyFileName  = "public-tls-server.key"

	// certificateValidity 825 天是 CA/Browser Forum 对叶子证书的上限量级，
	// 自签场景按它生成，避免手机在多年后才发现证书过期。
	certificateValidity = 825 * 24 * time.Hour
	// renewWhenLessThan 是「还剩不到 30 天就换一张」的提前量。证书一换指纹就变，
	// 太晚换会让手机在配对有效期内连不上。
	renewWhenLessThan = 30 * 24 * time.Hour

	// commonNameLimit 是 x509 里 CN 的保守长度，超出的名字只进 SAN。
	commonNameLimit = 64
)

// ErrInvalidMaterial 表示磁盘上的证书/私钥已损坏或与 SAN 不匹配，需要重新生成。
var ErrInvalidMaterial = errors.New("公网 TLS 证书不可用")

// Material 是一张可用的自签证书及其对外信任依据。
type Material struct {
	Certificate tls.Certificate
	// Fingerprint 是证书 DER 的 SHA-256（base64）。iOS 端按同一算法对
	// SecCertificateCopyData 做 SHA-256 后比对，因此换证书必须重新配对。
	Fingerprint string
	CertPath    string
	KeyPath     string
	// Reused 表示沿用了磁盘上已有的可用证书，调用方据此决定是否提示「指纹未变」。
	Reused bool
}

// TLSConfig 返回只服务 HTTP/1.1 与 h2 的服务端配置。
func (m Material) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.Certificate},
		MinVersion:   tls.VersionTLS12,
	}
}

// Ensure 加载或按需生成配置目录里的自签证书。hosts 接受 IP 与 DNS 名，
// 顺序无关；已有证书覆盖同一组 SAN 且未临近过期时直接复用，保证指纹稳定。
func Ensure(dir string, hosts []string) (Material, error) {
	dnsNames, ipAddresses, err := splitHosts(hosts)
	if err != nil {
		return Material{}, err
	}
	if len(dnsNames) == 0 && len(ipAddresses) == 0 {
		return Material{}, fmt.Errorf("%w：公网 TLS 证书需要至少一个 IP 或域名", ErrInvalidMaterial)
	}
	certPath := filepath.Join(dir, CertificateFileName)
	keyPath := filepath.Join(dir, PrivateKeyFileName)
	if material, err := load(certPath, keyPath); err == nil {
		if certificateUsable(material.Certificate.Leaf, dnsNames, ipAddresses) {
			material.Reused = true
			return material, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrInvalidMaterial) {
		return Material{}, err
	}
	return generate(certPath, keyPath, dnsNames, ipAddresses)
}

// Fingerprint 返回证书 DER 的 base64 SHA-256。
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Load 读取用户自备的正式证书与私钥，不做生成也不检查 SAN 覆盖。
// 正式证书由 CA 签发，手机可直接走系统信任链，指纹只作为额外核对信息。
func Load(certPath, keyPath string) (Material, error) {
	material, err := load(certPath, keyPath)
	if err != nil {
		return Material{}, err
	}
	return material, nil
}

func load(certPath, keyPath string) (Material, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return Material{}, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return Material{}, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return Material{}, fmt.Errorf("%w：%w", ErrInvalidMaterial, err)
	}
	if len(pair.Certificate) == 0 {
		return Material{}, ErrInvalidMaterial
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Material{}, fmt.Errorf("%w：%w", ErrInvalidMaterial, err)
	}
	pair.Leaf = leaf
	return Material{
		Certificate: pair,
		Fingerprint: Fingerprint(leaf.Raw),
		CertPath:    certPath,
		KeyPath:     keyPath,
	}, nil
}

// certificateUsable 同时检查有效期与 SAN 覆盖：公网 IP 变了必须重签，
// 否则手机按新 IP 连接时证书校验直接失败，配对指纹也对不上。
func certificateUsable(leaf *x509.Certificate, dnsNames []string, ipAddresses []net.IP) bool {
	if leaf == nil {
		return false
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.Add(renewWhenLessThan).After(leaf.NotAfter) {
		return false
	}
	if !containsAll(leaf.DNSNames, dnsNames) {
		return false
	}
	for _, wanted := range ipAddresses {
		found := false
		for _, have := range leaf.IPAddresses {
			if have.Equal(wanted) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func generate(certPath, keyPath string, dnsNames []string, ipAddresses []net.IP) (Material, error) {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return Material{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Material{}, fmt.Errorf("生成公网 TLS 私钥失败：%w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Material{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      commonName(dnsNames, ipAddresses),
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(certificateValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// 自签即自证：BasicConstraints 让验证方把它当作可信根使用，
		// 手机侧只需把这张证书本身加入临时信任链即可通过校验。
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return Material{}, fmt.Errorf("签发公网 TLS 证书失败：%w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return Material{}, err
	}
	// 私钥只允许当前用户读写：配置目录里同时存着 auth.token，权限收紧是底线。
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return Material{}, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return Material{}, err
	}
	material, err := load(certPath, keyPath)
	if err != nil {
		return Material{}, err
	}
	return material, nil
}

// commonName 选一个可读名字填 CN。公网 IP 直连时 CN 不参与校验，
// 但填得可读能让 doctor 与 openssl 输出更容易核对。
func commonName(dnsNames []string, ipAddresses []net.IP) pkix.Name {
	name := pkix.Name{Organization: []string{"Mimi Remote"}}
	candidate := ""
	if len(dnsNames) > 0 {
		candidate = dnsNames[0]
	} else if len(ipAddresses) > 0 {
		candidate = ipAddresses[0].String()
	}
	if len(candidate) > commonNameLimit {
		candidate = candidate[:commonNameLimit]
	}
	if candidate != "" {
		name.CommonName = candidate
	}
	return name
}

func splitHosts(hosts []string) ([]string, []net.IP, error) {
	dnsSet := map[string]bool{}
	ips := []net.IP{}
	for _, raw := range hosts {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			if !containsIP(ips, ip) {
				ips = append(ips, ip)
			}
			continue
		}
		if host == "localhost" {
			dnsSet[host] = true
			continue
		}
		if strings.ContainsAny(host, " \t/:") || !strings.Contains(host, ".") {
			// 带端口或不是合法 DNS 名的输入直接报错，避免把 "1.2.3.4:8443" 当成主机名。
			return nil, nil, fmt.Errorf("%w：公网地址 %q 既不是 IP 也不是域名", ErrInvalidMaterial, raw)
		}
		dnsSet[host] = true
	}
	dnsNames := make([]string, 0, len(dnsSet))
	for name := range dnsSet {
		dnsNames = append(dnsNames, name)
	}
	sort.Strings(dnsNames)
	sort.Slice(ips, func(i, j int) bool { return ips[i].String() < ips[j].String() })
	return dnsNames, ips, nil
}

func containsIP(ips []net.IP, wanted net.IP) bool {
	for _, ip := range ips {
		if ip.Equal(wanted) {
			return true
		}
	}
	return false
}

func containsAll(have []string, wanted []string) bool {
	set := map[string]bool{}
	for _, item := range have {
		set[strings.ToLower(item)] = true
	}
	for _, item := range wanted {
		if !set[item] {
			return false
		}
	}
	return true
}

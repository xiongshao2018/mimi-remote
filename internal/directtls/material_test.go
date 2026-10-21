package directtls

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureGeneratesSelfSignedCertificateWithPublicSAN(t *testing.T) {
	dir := t.TempDir()
	material, err := Ensure(dir, []string{"203.0.113.9", "mimi.example.com", "203.0.113.9"})
	if err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	if material.Reused {
		t.Fatal("首次生成不应标记 Reused")
	}
	if material.Fingerprint == "" {
		t.Fatal("缺少证书指纹")
	}
	leaf := material.Certificate.Leaf
	if leaf == nil {
		t.Fatal("证书缺少 Leaf")
	}
	foundIP := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("203.0.113.9")) {
			foundIP = true
		}
	}
	if !foundIP {
		t.Fatalf("公网 IP 未写进 SAN：%v", leaf.IPAddresses)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "mimi.example.com" {
		t.Fatalf("DNS SAN 不正确：%v", leaf.DNSNames)
	}
	if leaf.NotAfter.Before(time.Now().Add(800 * 24 * time.Hour)) {
		t.Fatalf("证书有效期过短：%s", leaf.NotAfter)
	}
	if leaf.BasicConstraintsValid && !leaf.IsCA {
		t.Fatal("自签证书必须自证为根")
	}
	if got := Fingerprint(leaf.Raw); got != material.Fingerprint {
		t.Fatalf("指纹不一致：got=%s want=%s", got, material.Fingerprint)
	}
}

func TestEnsureKeepsFingerprintStableAndReissuesOnAddressChange(t *testing.T) {
	dir := t.TempDir()
	first, err := Ensure(dir, []string{"203.0.113.9"})
	if err != nil {
		t.Fatalf("首次 Ensure 失败：%v", err)
	}
	reused, err := Ensure(dir, []string{"203.0.113.9", "203.0.113.9"})
	if err != nil {
		t.Fatalf("复用 Ensure 失败：%v", err)
	}
	if !reused.Reused || reused.Fingerprint != first.Fingerprint {
		t.Fatalf("同一组 SAN 应复用证书：reused=%t fingerprint=%s/%s", reused.Reused, reused.Fingerprint, first.Fingerprint)
	}
	rotated, err := Ensure(dir, []string{"203.0.113.9", "2001:db8::1"})
	if err != nil {
		t.Fatalf("换地址 Ensure 失败：%v", err)
	}
	if rotated.Reused || rotated.Fingerprint == first.Fingerprint {
		t.Fatal("公网地址变化后必须重签并更换指纹")
	}
}

func TestEnsureRejectsUnusableMaterialOnDisk(t *testing.T) {
	dir := t.TempDir()
	if _, err := Ensure(dir, []string{"203.0.113.9"}); err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, CertificateFileName), []byte("not a pem"), 0o644); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := Ensure(dir, []string{"203.0.113.9"})
	if err != nil {
		t.Fatalf("损坏证书应被重新生成覆盖：%v", err)
	}
	if rebuilt.Reused {
		t.Fatal("损坏证书不应被复用")
	}
}

func TestEnsureRejectsHostWithPort(t *testing.T) {
	if _, err := Ensure(t.TempDir(), []string{"203.0.113.9:8443"}); err == nil {
		t.Fatal("带端口的地址应被拒绝")
	}
}

// TestMaterialServesHandshakeWithPinnedPeer 复现手机侧的用法：系统信任链里没有这张证书，
// 客户端禁用默认校验后只信任这一张，握手仍必须成功。
func TestMaterialServesHandshakeWithPinnedPeer(t *testing.T) {
	material, err := Ensure(t.TempDir(), []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", material.TLSConfig())
	if err != nil {
		t.Fatalf("TLS 监听失败：%v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.(*tls.Conn).Handshake()
		_ = conn.Close()
	}()

	// 手机侧等价做法：系统信任链里没有这张证书，只把配对时指纹命中的那一张放进临时信任链。
	pool := x509.NewCertPool()
	pool.AddCert(material.Certificate.Leaf)
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		RootCAs:    pool,
		ServerName: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("按证书指纹建立的信任链握手失败：%v", err)
	}
	defer conn.Close()
	if conn.ConnectionState().PeerCertificates[0].Raw == nil {
		t.Fatal("握手未取回证书 DER")
	}
}

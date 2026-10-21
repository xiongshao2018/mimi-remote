package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gaixianggeng/mimi-remote/internal/auth"
	"github.com/gaixianggeng/mimi-remote/internal/config"
)

const testPublicFingerprint = "ZmluZ2VycHJpbnQ="

// TestPairingClaimHonorsPublicTLSPin 覆盖公网配对的信任链：指纹参与签名，
// 中间人替换或抹掉指纹都必须失败，成功响应再把当前指纹回给手机核对。
func TestPairingClaimHonorsPublicTLSPin(t *testing.T) {
	server := newTestServer(t)
	server.router.SetTLSFingerprint(testPublicFingerprint)
	now := time.Now().UTC()
	endpoint := "https://203.0.113.9:8443"
	ticket := auth.NewPairingTicketWithPin(endpoint, testToken, testPublicFingerprint, now.Add(-time.Minute), now.Add(9*time.Minute))
	payload := pairingClaimRequest{
		Endpoint:  ticket.Endpoint,
		IssuedAt:  ticket.IssuedAt,
		ExpiresAt: ticket.ExpiresAt,
		Signature: ticket.Signature,
		TLSPin:    ticket.TLSPin,
	}

	response := performPairingClaim(t, server.handler, payload)
	if response.Code != http.StatusOK {
		t.Fatalf("带指纹的配对票据应兑换成功：code=%d body=%s", response.Code, response.Body.String())
	}
	var claimed pairingClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.TLSPin != testPublicFingerprint {
		t.Fatalf("响应必须回显当前证书指纹：%+v", claimed)
	}

	for name, mutate := range map[string]func(*pairingClaimRequest){
		"替换指纹": func(body *pairingClaimRequest) { body.TLSPin = "ZmluZ2VycHJpbnU=" },
		"抹掉指纹": func(body *pairingClaimRequest) { body.TLSPin = "" },
	} {
		tampered := payload
		mutate(&tampered)
		if code := performPairingClaim(t, server.handler, tampered).Code; code != http.StatusUnauthorized {
			t.Fatalf("%s 后必须拒绝兑换，got=%d", name, code)
		}
	}

	// 旧三字段票据（无公网入口的主机）继续可兑换。
	legacy := auth.NewPairingTicket("http://100.64.0.1:8787", testToken, now.Add(-time.Minute), now.Add(9*time.Minute))
	legacyPayload := pairingClaimRequest{
		Endpoint:  legacy.Endpoint,
		IssuedAt:  legacy.IssuedAt,
		ExpiresAt: legacy.ExpiresAt,
		Signature: legacy.Signature,
	}
	if code := performPairingClaim(t, server.handler, legacyPayload).Code; code != http.StatusOK {
		t.Fatalf("旧格式配对票据必须继续可兑换，got=%d", code)
	}
}

// TestModuleStatusReportsPublicEntrypoint 让 iOS 设置页能区分「公网已开」和
// 「公网已开但还没配地址」，而不是把不可用的入口显示成可连接。
func TestModuleStatusReportsPublicEntrypoint(t *testing.T) {
	server := newTestServerWithConfig(t, func(cfg *config.Config) {
		cfg.Network.AllowTailscale = boolPtr(false)
		cfg.Network.Public = &config.PublicAccessConfig{Enabled: boolPtr(true), Advertise: "203.0.113.9"}
	})
	server.router.SetTLSFingerprint(testPublicFingerprint)

	request := httptest.NewRequest(http.MethodGet, "/api/host/modules", nil)
	request.RemoteAddr = "127.0.0.1:51234"
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("本机模块状态查询应成功：code=%d body=%s", response.Code, response.Body.String())
	}
	var status config.ModuleStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.PublicEnabled || !status.PublicAvailable {
		t.Fatalf("公网入口应报告可用：%+v", status)
	}
	if status.PublicEndpoint != "https://203.0.113.9:8443" {
		t.Fatalf("公网地址异常：%s", status.PublicEndpoint)
	}
	if status.PublicFingerprint != testPublicFingerprint {
		t.Fatalf("模块状态必须报告运行中监听的指纹：%+v", status)
	}
	if status.LANEnabled || status.TailscaleEnabled {
		t.Fatalf("公网开关不应改动其它通道：%+v", status)
	}
}

func TestModuleStatusWithoutPublicEntrypointKeepsPublicFieldsOff(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/host/modules", nil)
	request.RemoteAddr = "127.0.0.1:51234"
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	server.handler.ServeHTTP(response, request)
	var status config.ModuleStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.PublicEnabled || status.PublicAvailable || status.PublicEndpoint != "" || status.PublicFingerprint != "" {
		t.Fatalf("未开启公网时不应出现公网状态：%+v", status)
	}
}

func boolPtr(value bool) *bool { return &value }

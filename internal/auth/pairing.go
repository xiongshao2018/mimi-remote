package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type PairingTicket struct {
	Endpoint  string `json:"endpoint"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	Signature string `json:"pair_sig"`
	// TLSPin 是公网 HTTPS 入口的证书指纹（base64 SHA-256 of certificate DER）。
	// 它参与签名：二维码是唯一的带外信道，指纹不签进去，链路上的中间人就能把指纹
	// 换成自己那张自签证书，手机照样连上但 Token 已被读走。
	// 空值表示这次配对没有公网 TLS 入口，签名沿用三字段的旧格式。
	TLSPin string `json:"tls_pin,omitempty"`
}

func NewPairingTicket(endpoint string, secret string, issuedAt time.Time, expiresAt time.Time) PairingTicket {
	return NewPairingTicketWithPin(endpoint, secret, "", issuedAt, expiresAt)
}

// NewPairingTicketWithPin 生成带证书指纹的票据。指纹为空时签名格式与旧版本完全一致，
// 因此旧客户端扫旧二维码、新客户端扫旧二维码都不会因为多一个字段而验签失败。
func NewPairingTicketWithPin(endpoint string, secret string, pin string, issuedAt time.Time, expiresAt time.Time) PairingTicket {
	ticket := PairingTicket{
		Endpoint: strings.TrimSpace(endpoint),
		TLSPin:   strings.TrimSpace(pin),
		// 保留亚秒精度，确保用户连续刷新二维码时仍生成可区分的新票据，
		// 也便于按每张票据自己的签发和过期时间审计生命周期。
		// time.Parse(RFC3339Nano) 仍兼容旧版本生成的不含小数秒 RFC3339 时间。
		IssuedAt:  issuedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano),
	}
	ticket.Signature = SignPairingTicketWithPin(secret, ticket.Endpoint, ticket.IssuedAt, ticket.ExpiresAt, ticket.TLSPin)
	return ticket
}

func SignPairingTicket(secret string, endpoint string, issuedAt string, expiresAt string) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	mac.Write([]byte(strings.TrimSpace(endpoint)))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strings.TrimSpace(issuedAt)))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strings.TrimSpace(expiresAt)))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignPairingTicketWithPin 在旧三字段之后追加指纹。只有指纹非空才走这条路径，
// 且必须放在验签的第一位：中间人把 tls_pin 抹掉后旧格式验签同样过不了。
func SignPairingTicketWithPin(secret string, endpoint string, issuedAt string, expiresAt string, pin string) string {
	if strings.TrimSpace(pin) == "" {
		return SignPairingTicket(secret, endpoint, issuedAt, expiresAt)
	}
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	mac.Write([]byte(strings.TrimSpace(endpoint)))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strings.TrimSpace(issuedAt)))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strings.TrimSpace(expiresAt)))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(strings.TrimSpace(pin)))
	return hex.EncodeToString(mac.Sum(nil))
}

func ValidatePairingTicket(secret string, ticket PairingTicket, now time.Time) error {
	secret = strings.TrimSpace(secret)
	endpoint := strings.TrimSpace(ticket.Endpoint)
	issuedAt := strings.TrimSpace(ticket.IssuedAt)
	expiresAt := strings.TrimSpace(ticket.ExpiresAt)
	signature := strings.TrimSpace(ticket.Signature)
	pin := strings.TrimSpace(ticket.TLSPin)
	if secret == "" {
		return fmt.Errorf("配对不可用：auth.token 未配置")
	}
	if endpoint == "" || issuedAt == "" || expiresAt == "" || signature == "" {
		return fmt.Errorf("配对票据缺少必要字段")
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return fmt.Errorf("配对票据 expires_at 无效")
	}
	if !now.Before(expiry) {
		return fmt.Errorf("配对二维码已过期")
	}
	issued, err := time.Parse(time.RFC3339Nano, issuedAt)
	if err != nil {
		return fmt.Errorf("配对票据 issued_at 无效")
	}
	if issued.After(now.Add(2 * time.Minute)) {
		return fmt.Errorf("配对票据 issued_at 超出允许范围")
	}
	expected := SignPairingTicket(secret, endpoint, issuedAt, expiresAt)
	if pin != "" {
		expected = SignPairingTicketWithPin(secret, endpoint, issuedAt, expiresAt, pin)
	}
	if subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) != 1 {
		return fmt.Errorf("配对票据签名无效")
	}
	return nil
}

package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPairingTicketValidatesSignatureAndExpiry(t *testing.T) {
	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	ticket := NewPairingTicket("http://100.64.0.1:8787", "0123456789abcdef0123456789abcdef", now, now.Add(10*time.Minute))

	if err := ValidatePairingTicket("0123456789abcdef0123456789abcdef", ticket, now.Add(time.Minute)); err != nil {
		t.Fatalf("合法配对票据应通过校验：%v", err)
	}

	ticket.Endpoint = "http://100.64.0.2:8787"
	if err := ValidatePairingTicket("0123456789abcdef0123456789abcdef", ticket, now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "签名") {
		t.Fatalf("篡改 endpoint 应导致签名失败，got=%v", err)
	}
}

func TestPairingTicketRejectsExpiredTicket(t *testing.T) {
	now := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	ticket := NewPairingTicket("http://100.64.0.1:8787", "0123456789abcdef0123456789abcdef", now.Add(-20*time.Minute), now.Add(-10*time.Minute))

	if err := ValidatePairingTicket("0123456789abcdef0123456789abcdef", ticket, now); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("过期配对票据应被拒绝，got=%v", err)
	}
}

func TestPairingTicketRejectsAtExactExpiryAfterRepeatedValidation(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	issuedAt := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	expiresAt := issuedAt.Add(10 * time.Minute)
	ticket := NewPairingTicket("http://100.64.0.1:8787", secret, issuedAt, expiresAt)

	for attempt := 1; attempt <= 2; attempt++ {
		if err := ValidatePairingTicket(secret, ticket, expiresAt.Add(-time.Nanosecond)); err != nil {
			t.Fatalf("到期前第 %d 次校验应成功：%v", attempt, err)
		}
	}
	for _, now := range []time.Time{expiresAt, expiresAt.Add(time.Nanosecond)} {
		if err := ValidatePairingTicket(secret, ticket, now); err == nil || !strings.Contains(err.Error(), "过期") {
			t.Fatalf("到期瞬间及之后必须拒绝：now=%s err=%v", now, err)
		}
	}
}

func TestPairingTicketUsesSubsecondPrecisionAndAcceptsLegacyTimestamp(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	base := time.Date(2026, 7, 13, 8, 0, 0, 100, time.UTC)
	first := NewPairingTicket("http://100.64.0.1:8787", secret, base, base.Add(10*time.Minute))
	second := NewPairingTicket("http://100.64.0.1:8787", secret, base.Add(200*time.Nanosecond), base.Add(10*time.Minute+200*time.Nanosecond))
	if first.Signature == second.Signature {
		t.Fatal("同一秒内刷新生成的票据必须可区分")
	}

	legacyIssued := base.Format(time.RFC3339)
	legacyExpires := base.Add(10 * time.Minute).Format(time.RFC3339)
	legacy := PairingTicket{
		Endpoint:  "http://100.64.0.1:8787",
		IssuedAt:  legacyIssued,
		ExpiresAt: legacyExpires,
		Signature: SignPairingTicket(secret, "http://100.64.0.1:8787", legacyIssued, legacyExpires),
	}
	if err := ValidatePairingTicket(secret, legacy, base.Add(time.Minute)); err != nil {
		t.Fatalf("升级后仍应接受有效的旧 RFC3339 票据：%v", err)
	}
}

func TestPairingTicketPinIsSignedAndFailClosed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(5 * time.Minute)
	const secret = "token"
	const endpoint = "https://203.0.113.9:8443"
	const pin = "9hEnw7pLl6lGj8sKk0nM5tq3rVQz1bC7dYfXa2eWuio="

	ticket := NewPairingTicketWithPin(endpoint, secret, pin, now, expires)
	if err := ValidatePairingTicket(secret, ticket, now); err != nil {
		t.Fatalf("带指纹的票据应通过验签：%v", err)
	}

	// 中间人抹掉 tls_pin：签名字段是按含指纹的规范算出来的，旧格式验签必须失败。
	stripped := ticket
	stripped.TLSPin = ""
	if err := ValidatePairingTicket(secret, stripped, now); err == nil {
		t.Fatal("抹掉证书指纹后必须拒绝")
	}

	// 中间人换成自己的指纹：没有 Token 就算不出新签名。
	swapped := ticket
	swapped.TLSPin = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if err := ValidatePairingTicket(secret, swapped, now); err == nil {
		t.Fatal("替换证书指纹后必须拒绝")
	}

	// 旧版本生成的三字段票据继续可验，公网模块上线不能打断既有配对。
	legacy := NewPairingTicket(endpoint, secret, now, expires)
	if legacy.TLSPin != "" {
		t.Fatal("无公网入口时不应带指纹")
	}
	if err := ValidatePairingTicket(secret, legacy, now); err != nil {
		t.Fatalf("旧格式票据应继续通过：%v", err)
	}
	if err := ValidatePairingTicket(secret, PairingTicket{
		Endpoint: legacy.Endpoint, IssuedAt: legacy.IssuedAt, ExpiresAt: legacy.ExpiresAt,
		Signature: legacy.Signature, TLSPin: pin,
	}, now); err == nil {
		t.Fatal("旧格式签名不能配上新格式指纹")
	}
}

import Foundation
import XCTest
@testable import MimiRemote

/// 公网直连的唯一信任依据是证书指纹。这里锁定三处最容易漂移的地方：指纹编码、
/// 地址归一化和指纹表的生命周期。任何一处出错都会让手机连不上公网宿主，
/// 或者把已经撤销的信任重新点亮。
final class TLSCertificatePinningTests: XCTestCase {
    /// SHA-256 of empty input，base64。与 agentd 侧 base64(SHA-256(DER)) 同一编码。
    private let fingerprint = "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="
    private let publicEndpoint = "https://203.0.113.9:8443"

    func testFingerprintMatchesSHA256Base64Encoding() {
        XCTAssertEqual(TLSCertificatePinning.fingerprintBase64(Data()), fingerprint)
    }

    func testNormalizedPinOnlyAcceptsBase64SHA256() {
        XCTAssertEqual(TLSCertificatePinning.normalizedPin(" \(fingerprint) "), fingerprint)
        XCTAssertNil(TLSCertificatePinning.normalizedPin(nil))
        XCTAssertNil(TLSCertificatePinning.normalizedPin(""))
        XCTAssertNil(TLSCertificatePinning.normalizedPin("not-a-fingerprint"))
        XCTAssertNil(TLSCertificatePinning.normalizedPin(Data(repeating: 0, count: 31).base64EncodedString()))
        XCTAssertNil(TLSCertificatePinning.normalizedPin(Data(repeating: 0, count: 33).base64EncodedString()))
    }

    func testHostFromEndpointIgnoresPortAndLetterCase() {
        XCTAssertEqual(TLSCertificatePinning.host(fromEndpoint: "https://Example.COM:8443"), "example.com")
        XCTAssertEqual(TLSCertificatePinning.host(fromEndpoint: publicEndpoint), "203.0.113.9")
        XCTAssertEqual(TLSCertificatePinning.host(fromEndpoint: "https://[2001:DB8::1]:8443"), "2001:db8::1")
        XCTAssertNil(TLSCertificatePinning.host(fromEndpoint: ""))
    }

    func testPendingPinSurvivesProfileRebuildUntilProfileAdoptsIt() {
        let table = TLSCertificatePinTable()
        XCTAssertTrue(table.registerPendingPin(fingerprint, forEndpoint: publicEndpoint))
        XCTAssertEqual(table.pin(forEndpoint: publicEndpoint), fingerprint)

        // 配对途中的档案重建不能把还没落进档案的指纹当成孤儿抹掉。
        table.replaceAll([(endpoint: "http://192.168.1.10:8787", pin: nil)])
        XCTAssertEqual(table.pin(forEndpoint: publicEndpoint), fingerprint)

        // 档案接管之后信任归档案所有：删除档案必须同时撤销指纹。
        table.replaceAll([(endpoint: publicEndpoint, pin: fingerprint)])
        XCTAssertEqual(table.pin(forEndpoint: publicEndpoint), fingerprint)
        table.replaceAll([])
        XCTAssertNil(table.pin(forEndpoint: publicEndpoint))
    }

    func testRegisterPendingPinRejectsUnusableValues() {
        let table = TLSCertificatePinTable()
        XCTAssertFalse(table.registerPendingPin("not-a-fingerprint", forEndpoint: publicEndpoint))
        XCTAssertFalse(table.registerPendingPin(fingerprint, forEndpoint: "not a url"))
        XCTAssertNil(table.pin(forEndpoint: publicEndpoint))
    }

    func testPairingTicketCarriesFingerprintIntoClaimRequest() throws {
        let ticket = PairingTicket(
            endpoint: publicEndpoint,
            issuedAt: "1764000000",
            expiresAt: "1764000900",
            pairSignature: "sig",
            tlsCertPin: fingerprint
        )
        XCTAssertEqual(ticket.claimRequest.tlsPin, fingerprint)
        XCTAssertEqual(ticket.claimRequest(tailcatClientKey: "client-key").tlsPin, fingerprint)

        let encoded = try JSONEncoder().encode(ticket.claimRequest)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: encoded) as? [String: Any])
        XCTAssertEqual(object["tls_pin"] as? String, fingerprint)
    }

    func testLegacyPairingTicketOmitsFingerprintKey() throws {
        let legacy = PairingTicket(
            endpoint: "http://192.168.1.10:8787",
            issuedAt: "1764000000",
            expiresAt: "1764000900",
            pairSignature: "sig"
        )
        XCTAssertNil(legacy.tlsCertPin)

        let encoded = try JSONEncoder().encode(legacy.claimRequest)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: encoded) as? [String: Any])
        XCTAssertFalse(object.keys.contains("tls_pin"))
    }

    func testCommitKeepsFingerprintOnlyForTheSameHost() {
        let existing = ConnectionProfile(
            id: "profile-1",
            displayName: "Host",
            endpoint: publicEndpoint,
            lastSuccessfulAt: nil,
            tlsCertPin: fingerprint
        )
        // 切换电脑、改名、重连这些路径没有新指纹，host 未变时沿用旧值。
        let sameHost = PreparedConnectionSettings(endpoint: publicEndpoint, token: "token")
        XCTAssertEqual(AppStore.resolvedTLSCertPin(prepared: sameHost, existing: existing), fingerprint)

        // 地址换到另一台宿主时旧指纹必须作废，否则新宿主被旧指纹锁死。
        let otherHost = PreparedConnectionSettings(endpoint: "https://203.0.113.10:8443", token: "token")
        XCTAssertNil(AppStore.resolvedTLSCertPin(prepared: otherHost, existing: existing))

        // 重新扫码带来新指纹时以新指纹为准。
        let rotated = Data(repeating: 7, count: 32).base64EncodedString()
        let rotatedPrepared = PreparedConnectionSettings(
            endpoint: publicEndpoint,
            token: "token",
            tlsCertPin: rotated
        )
        XCTAssertEqual(AppStore.resolvedTLSCertPin(prepared: rotatedPrepared, existing: existing), rotated)
    }
}

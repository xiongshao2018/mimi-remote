import CryptoKit
import Foundation
import Security

// 公网直连时 agentd 只有本机自签证书：手机按公网 IP 连接时，系统信任链里没有任何 CA
// 能验通它；而「信任任意证书」又允许中间人换掉证书后窃听访问码。
//
// 因此公网入口的信任依据是配对二维码里带外下发的证书指纹，即
// base64(SHA-256(certificate DER))。本文件负责两件事：
//   1. 保存 host 到指纹的映射，指纹来自二维码或 claim 响应，并随连接档案持久化；
//   2. 在 TLS 挑战里先比对指纹，再把证书链锚定到那张被指纹确认的证书上，继续交给
//      系统校验有效期与主机名；任何一步不通过都直接拒绝连接（fail-closed）。
//
// 没有登记指纹的 host（局域网 HTTP、Tailscale、Harness 中继）走系统默认处理，
// 现有链路行为不变。

enum TLSCertificatePinning {
    /// 与 agentd 侧一致的指纹编码：base64(SHA-256(证书 DER))，标准 base64 含填充。
    static func fingerprintBase64(_ certificateData: Data) -> String {
        Data(SHA256.hash(data: certificateData)).base64EncodedString()
    }

    static func normalizedHost(_ host: String) -> String {
        host.trimmingCharacters(in: CharacterSet(charactersIn: "[]").union(.whitespacesAndNewlines))
            .lowercased()
    }

    /// 从 endpoint 取用于查指纹的 host。档案里的 endpoint 已由 EndpointTransportPolicy
    /// 归一化，这里只做容错解析，不重复校验地址合法性。
    static func host(fromEndpoint raw: String) -> String? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        if let components = URLComponents(string: trimmed), let host = components.host, !host.isEmpty {
            return normalizedHost(host)
        }
        if let url = URL(string: trimmed), let host = url.host, !host.isEmpty {
            return normalizedHost(host)
        }
        return nil
    }

    /// 只接受标准 base64 且解码后正好 32 字节的值，避免把误填的字符串当成指纹。
    static func normalizedPin(_ raw: String?) -> String? {
        guard let trimmed = raw?.trimmingCharacters(in: .whitespacesAndNewlines),
              !trimmed.isEmpty,
              trimmed.count <= 64,
              let decoded = Data(base64Encoded: trimmed),
              decoded.count == 32 else {
            return nil
        }
        return decoded.base64EncodedString()
    }
}

/// host 到证书指纹的映射。公网 TLS 挑战发生在任意请求线程上，因此所有访问都在锁内完成。
final class TLSCertificatePinTable {
    static let shared = TLSCertificatePinTable()

    private let lock = NSLock()
    private var pinsByHost: [String: String] = [:]
    /// 扫码后、档案提交前登记的指纹。配对期间的 claim 与探测请求必须已经能查到它，
    /// 而这段时间发生的档案重建不能把它当成孤儿抹掉。
    private var pendingPinsByHost: [String: String] = [:]

    /// 生产代码只用 `shared`；测试用独立实例验证指纹生命周期，互不污染。
    init() {}

    func pin(forHost host: String?) -> String? {
        guard let host, !host.isEmpty else { return nil }
        lock.lock()
        defer { lock.unlock() }
        return pinsByHost[host] ?? pendingPinsByHost[host]
    }

    func pin(forEndpoint endpoint: String?) -> String? {
        guard let endpoint else { return nil }
        return pin(forHost: TLSCertificatePinning.host(fromEndpoint: endpoint))
    }

    /// 登记尚未落进档案的指纹。返回 false 表示指纹为空或地址解析失败，调用方按旧行为继续。
    @discardableResult
    func registerPendingPin(_ pin: String?, forEndpoint endpoint: String) -> Bool {
        guard let normalized = TLSCertificatePinning.normalizedPin(pin),
              let host = TLSCertificatePinning.host(fromEndpoint: endpoint) else {
            return false
        }
        lock.lock()
        defer { lock.unlock() }
        pinsByHost[host] = normalized
        pendingPinsByHost[host] = normalized
        return true
    }

    /// 用当前连接档案重建指纹表：档案删除即指纹删除，避免同一公网 IP 被新宿主复用时
    /// 被旧指纹锁死。尚未被档案接管的待生效指纹保留。
    func replaceAll(_ entries: [(endpoint: String, pin: String?)]) {
        var rebuilt: [String: String] = [:]
        for entry in entries {
            guard let normalized = TLSCertificatePinning.normalizedPin(entry.pin),
                  let host = TLSCertificatePinning.host(fromEndpoint: entry.endpoint) else {
                continue
            }
            rebuilt[host] = normalized
        }
        lock.lock()
        defer { lock.unlock() }
        for host in Array(pendingPinsByHost.keys) where rebuilt[host] != nil {
            pendingPinsByHost.removeValue(forKey: host)
        }
        for (host, pin) in pendingPinsByHost {
            rebuilt[host] = pin
        }
        pinsByHost = rebuilt
    }

    func snapshot() -> [String: String] {
        lock.lock()
        defer { lock.unlock() }
        return pinsByHost
    }
}

/// 公网 TLS 挑战处理器。只有登记过指纹的 host 才接管评估，其余一律交回系统默认处理。
final class TLSCertificatePinDelegate: NSObject, URLSessionDelegate, URLSessionTaskDelegate {
    static let shared = TLSCertificatePinDelegate()

    func urlSession(
        _ session: URLSession,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        handle(challenge, completionHandler: completionHandler)
    }

    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        handle(challenge, completionHandler: completionHandler)
    }

    private func handle(
        _ challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        let host = TLSCertificatePinning.normalizedHost(challenge.protectionSpace.host)
        guard let expectedPin = TLSCertificatePinTable.shared.pin(forHost: host) else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        guard let pinnedCertificate = matchedCertificate(in: trust, expectedPin: expectedPin) else {
            completionHandler(.cancelAuthenticationChallenge, nil)
            return
        }
        // 系统评估是异步的，而挑战的 completionHandler 允许稍后再调。这里不阻塞 delegate
        // 队列，避免评估回调需要主线程时与调用方互相等待。
        validates(trust, pinnedCertificate: pinnedCertificate, host: host) { isValid in
            if isValid {
                completionHandler(.useCredential, URLCredential(trust: trust))
            } else {
                completionHandler(.cancelAuthenticationChallenge, nil)
            }
        }
    }

    /// 指纹比对遍历整条链：agentd 之后可能改成叶子加中间 CA，任一环命中即视为配对确认。
    private func matchedCertificate(in trust: SecTrust, expectedPin: String) -> SecCertificate? {
        let count = SecTrustGetCertificateCount(trust)
        for index in 0..<count {
            guard let certificate = SecTrustGetCertificateAtIndex(trust, index) else { continue }
            let data = SecCertificateCopyData(certificate) as Data
            if TLSCertificatePinning.fingerprintBase64(data) == expectedPin {
                return certificate
            }
        }
        return nil
    }

    /// 指纹只证明「这就是配对时那张证书」，不证明它没过期、也不证明它对这个 host 有效。
    /// 这里重建一份把该证书作为唯一锚点的 trust，交给系统完成有效期与主机名校验。
    private func validates(
        _ trust: SecTrust,
        pinnedCertificate: SecCertificate,
        host: String,
        completion: @escaping (Bool) -> Void
    ) {
        var certificates: [SecCertificate] = []
        let count = SecTrustGetCertificateCount(trust)
        for index in 0..<count {
            if let certificate = SecTrustGetCertificateAtIndex(trust, index) {
                certificates.append(certificate)
            }
        }
        guard !certificates.isEmpty else {
            completion(false)
            return
        }
        let policy: SecPolicy = host.isEmpty
            ? SecPolicyCreateBasicX509()
            : SecPolicyCreateSSL(true, host as CFString)
        var anchoredTrust: SecTrust?
        guard SecTrustCreateWithCertificates(certificates as CFArray, policy, &anchoredTrust) == errSecSuccess,
              let evaluatedTrust = anchoredTrust,
              SecTrustSetAnchorCertificates(evaluatedTrust, [pinnedCertificate] as CFArray) == errSecSuccess,
              SecTrustSetAnchorCertificatesOnly(evaluatedTrust, true) == errSecSuccess else {
            completion(false)
            return
        }
        // 26.5 SDK 已移除 SecTrustEvaluateAsyncAtTimestamp，kSecTrustResult* 是 CF_ENUM、
        // Swift 里不导出全局常量。改用 iOS 13+ 的 SecTrustEvaluateAsyncWithError：
        // 其 Bool 语义就是「评估通过（unspecified/proceed）」，与原判定等价。
        // 头文件要求「MUST be called from that queue」，故整体切到 .main 再调用；
        // 评估本就异步，completionHandler 允许任意线程回调，行为不变。
        DispatchQueue.main.async {
            let status = SecTrustEvaluateAsyncWithError(evaluatedTrust, .main) { _, trusted, _ in
                completion(trusted)
            }
            if status != errSecSuccess {
                completion(false)
            }
        }
    }
}

/// 所有面向宿主 agentd 的 REST、WebSocket 与上传请求共用的 session。
///
/// delegate 必须是常驻单例：URLSession 强持有 delegate，而每个客户端各自建 session 会让
/// 指纹在连接建立前来不及登记。共用一个 session 也让 keep-alive 复用不受影响。
enum AgentTransport {
    static let pinDelegate = TLSCertificatePinDelegate.shared

    static let sharedSession: URLSession = {
        URLSession(configuration: URLSessionConfiguration.default, delegate: pinDelegate, delegateQueue: nil)
    }()

    /// 需要自定义超时或 Cookie 策略的调用方用它套上同一份 delegate。
    static func session(configuration: URLSessionConfiguration) -> URLSession {
        URLSession(configuration: configuration, delegate: pinDelegate, delegateQueue: nil)
    }
}

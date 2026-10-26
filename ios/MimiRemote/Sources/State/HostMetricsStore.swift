import Foundation
import Combine

/// 电脑硬件画像与实时指标的共享 Store。设备页读 `hardware` 摘要，
/// 会话输入框指标条读 `metrics`。两条通道独立刷新：硬件是静态的，
/// 连接建立后只拉一次；指标按固定间隔轮询，仅在会话页可见时运行，
/// 避免后台空转耗电。旧 agentd 没有对应端点时整块隐藏，不弹错误。
@MainActor
final class HostMetricsStore: ObservableObject {
    static let metricsPollingInterval: TimeInterval = 5

    @Published private(set) var hardware: HostHardwareResponse?
    @Published private(set) var metrics: HostMetricsResponse?
    /// 当前连接是否宣告支持硬件/指标端点。404 或解码失败后置 false，
    /// 本轮连接内不再重试；切换连接时重置。
    @Published private(set) var isSupported = true

    /// AppStore 注入的当前连接 client 工厂；与 LiveActivityCoordinator 同一模式。
    var makeClient: (() throws -> AgentAPIClient)?

    private var hardwareFetchedForFingerprint: String?
    private var pollingTask: Task<Void, Never>?
    private var hardwareTask: Task<Void, Never>?

    /// 连接指纹变化时重置支持状态并重新拉一次硬件画像。
    func bindConnection(fingerprint: String?) {
        guard fingerprint != hardwareFetchedForFingerprint else { return }
        hardwareFetchedForFingerprint = fingerprint
        hardware = nil
        metrics = nil
        isSupported = true
        if fingerprint != nil {
            refreshHardware()
        }
    }

    func refreshHardware() {
        guard isSupported, hardwareTask == nil, let makeClient else { return }
        hardwareTask = Task { [weak self, makeClient] in
            guard let self else { return }
            defer { self.hardwareTask = nil }
            do {
                let response = try await makeClient().hostHardware()
                self.hardware = response
            } catch {
                self.handleUnsupported(error)
            }
        }
    }

    /// 会话页出现时调用；重复调用幂等。
    func startMetricsPolling() {
        guard pollingTask == nil, isSupported, let makeClient else { return }
        pollingTask = Task { [weak self, makeClient] in
            while !Task.isCancelled {
                do {
                    let response = try await makeClient().hostMetrics()
                    if Task.isCancelled { return }
                    self?.metrics = response
                } catch {
                    if Task.isCancelled { return }
                    self?.handleUnsupported(error)
                    return
                }
                try? await Task.sleep(nanoseconds: UInt64(Self.metricsPollingInterval * 1_000_000_000))
            }
        }
    }

    /// 会话页消失时调用；指标条随之隐藏。
    func stopMetricsPolling() {
        pollingTask?.cancel()
        pollingTask = nil
    }

    private func handleUnsupported(_ error: Error) {
        // 404/405 = 旧 agentd；其余错误（超时、断连）不降级支持状态，
        // 下一次轮询或连接切换自然重试。
        if let apiError = error as? AgentAPIError, case .server(status: let status, _) = apiError,
           status == 404 || status == 405 {
            isSupported = false
            pollingTask?.cancel()
            pollingTask = nil
        }
    }
}

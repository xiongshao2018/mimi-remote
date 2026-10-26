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
    /// 可选外部推理引擎（Strata）的监控快照；未配置或拉不到时为 nil。
    @Published private(set) var strata: HostStrataResponse?
    /// 当前连接是否宣告支持硬件/指标端点。404 或解码失败后置 false，
    /// 本轮连接内不再重试；切换连接时重置。
    @Published private(set) var isSupported = true
    /// Strata 在本轮连接内是否宣告已配置。与 isSupported 分开：Strata 没
    /// 配置是正常状态，不该把硬件指标一起降级。
    private var isStrataConfigured = true

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
        strata = nil
        isSupported = true
        isStrataConfigured = true
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
                // Strata 是可选旁路：它失败不影响硬件指标继续轮询。
                await self?.refreshStrata(makeClient: makeClient)
                if Task.isCancelled { return }
                try? await Task.sleep(nanoseconds: UInt64(Self.metricsPollingInterval * 1_000_000_000))
            }
        }
    }

    /// 会话页消失时调用；指标条随之隐藏。
    func stopMetricsPolling() {
        pollingTask?.cancel()
        pollingTask = nil
    }

    /// 拉一次 Strata 快照。404 = 这台电脑没配置 Strata，本轮不再重试；
    /// 其余错误把快照标成不可用（保留可见性由视图决定），下轮自动恢复。
    private func refreshStrata(makeClient: () throws -> AgentAPIClient) async {
        guard isStrataConfigured else { return }
        do {
            let response = try await makeClient().hostStrata()
            if Task.isCancelled { return }
            strata = response
        } catch let apiError as AgentAPIError {
            if case .server(status: let status, _) = apiError, status == 404 || status == 405 {
                isStrataConfigured = false
                strata = nil
                return
            }
            if Task.isCancelled { return }
            strata = HostStrataResponse(available: false)
        } catch {
            if Task.isCancelled { return }
            strata = HostStrataResponse(available: false)
        }
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

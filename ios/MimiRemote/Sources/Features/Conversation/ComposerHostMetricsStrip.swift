import SwiftUI

/// 可选环境注入：生产 App 注入实例；快照测试不注入时读取方整块隐藏，
/// 不因缺失 EnvironmentObject 崩溃。
private struct HostMetricsStoreKey: EnvironmentKey {
    static let defaultValue: HostMetricsStore? = nil
}

extension EnvironmentValues {
    var hostMetricsStore: HostMetricsStore? {
        get { self[HostMetricsStoreKey.self] }
        set { self[HostMetricsStoreKey.self] = newValue }
    }
}

/// 会话输入框上方的电脑实时指标条：CPU / 内存 / 磁盘用量与网络上下行速度。
/// 数据来自 HostMetricsStore 对 agentd `/api/host/metrics` 的轮询；
/// 旧 agentd（404）或指标缺失时整条隐藏，不占输入区空间。
struct ComposerHostMetricsStrip: View {
    @EnvironmentObject private var themeStore: ThemeStore
    @Environment(\.hostMetricsStore) private var hostMetricsStore
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        let tokens = themeStore.tokens(for: colorScheme)

        if let metrics = hostMetricsStore?.metrics {
            HStack(spacing: 10) {
                metricChip(
                    icon: "cpu",
                    text: ByteFormat.formattedPercent(metrics.cpuUsagePercent),
                    label: L10n.text("ui.host_metric_cpu"),
                    tokens: tokens
                )
                metricChip(
                    icon: "memorychip",
                    text: usageText(used: metrics.memoryUsed, total: metrics.memoryTotal),
                    label: L10n.text("ui.host_metric_memory"),
                    tokens: tokens
                )
                metricChip(
                    icon: "internaldrive",
                    text: usageText(used: metrics.diskUsed, total: metrics.diskTotal),
                    label: L10n.text("ui.host_metric_disk"),
                    tokens: tokens
                )
                Spacer(minLength: 4)
                metricChip(
                    icon: "arrow.down.circle",
                    text: ByteFormat.formattedRate(metrics.networkRXBPS),
                    label: L10n.text("ui.host_metric_download"),
                    tokens: tokens
                )
                metricChip(
                    icon: "arrow.up.circle",
                    text: ByteFormat.formattedRate(metrics.networkTXBPS),
                    label: L10n.text("ui.host_metric_upload"),
                    tokens: tokens
                )
            }
            .padding(.horizontal, 4)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("composer.hostMetrics")
        }
    }

    private func usageText(used: UInt64, total: UInt64) -> String {
        guard total > 0 else { return "--" }
        let percent = Double(used) / Double(total) * 100
        return "\(ByteFormat.formattedPercent(percent)) · \(ByteFormat.formattedMemory(total))"
    }

    private func metricChip(
        icon: String,
        text: String,
        label: String,
        tokens: ThemeTokens
    ) -> some View {
        HStack(spacing: 3) {
            Image(systemName: icon)
                .font(themeStore.uiFont(.caption2))
            Text(text)
                .font(themeStore.uiFont(.caption2, weight: .medium))
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.85)
        }
        .foregroundStyle(tokens.secondaryText)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(label): \(text)")
    }
}

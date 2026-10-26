import SwiftUI

/// 设备页电脑行下方的监控卡片：上半是这台电脑的实时资源（GPU / CPU /
/// 内存 / 磁盘 / 网络），下半是可选的外部推理引擎（Strata）状态。
/// 数据来自 HostMetricsStore 对 agentd 的轮询；GPU 缺失、Strata 未配置
/// 时对应分块整块隐藏，不留空标题。
struct HostMonitorCard: View {
    @EnvironmentObject private var themeStore: ThemeStore
    @Environment(\.hostMetricsStore) private var hostMetricsStore
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        let tokens = themeStore.tokens(for: colorScheme)

        if let metrics = hostMetricsStore?.metrics {
            VStack(alignment: .leading, spacing: 8) {
                resourceSection(metrics, tokens: tokens)
                if let strata = hostMetricsStore?.strata, strata.available {
                    Divider().opacity(0.4)
                    strataSection(strata, tokens: tokens)
                }
            }
            .settingsRow(.descriptive)
            .accessibilityIdentifier("settings.profile.monitorCard")
        }
    }

    // MARK: - 电脑实时资源

    @ViewBuilder
    private func resourceSection(_ metrics: HostMetricsResponse, tokens: ThemeTokens) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            if metrics.gpu.ok {
                metricLine(
                    icon: "rectangle.on.rectangle",
                    title: metrics.gpu.name ?? L10n.text("ui.host_monitor_gpu"),
                    detail: gpuDetail(metrics.gpu),
                    tokens: tokens
                )
                usageBar(
                    fraction: fraction(metrics.gpu.utilPercent, limit: 100),
                    tokens: tokens
                )
                metricLine(
                    icon: "memorychip",
                    title: L10n.text("ui.host_monitor_vram"),
                    detail: vramDetail(metrics.gpu),
                    tokens: tokens
                )
                usageBar(
                    fraction: fraction(used: metrics.gpu.memUsedBytes, total: metrics.gpu.memTotalBytes),
                    tokens: tokens
                )
            }
            metricLine(
                icon: "cpu",
                title: L10n.text("ui.host_monitor_cpu"),
                detail: ByteFormat.formattedPercent(metrics.cpuUsagePercent),
                tokens: tokens
            )
            usageBar(fraction: fraction(metrics.cpuUsagePercent, limit: 100), tokens: tokens)

            metricLine(
                icon: "memorychip",
                title: L10n.text("ui.host_monitor_ram"),
                detail: usageDetail(used: metrics.memoryUsed, total: metrics.memoryTotal),
                tokens: tokens
            )
            usageBar(
                fraction: fraction(used: metrics.memoryUsed, total: metrics.memoryTotal),
                tokens: tokens
            )

            metricLine(
                icon: "internaldrive",
                title: L10n.text("ui.host_monitor_disk"),
                detail: usageDetail(used: metrics.diskUsed, total: metrics.diskTotal),
                tokens: tokens
            )
            usageBar(
                fraction: fraction(used: metrics.diskUsed, total: metrics.diskTotal),
                tokens: tokens
            )

            metricLine(
                icon: "arrow.down.circle",
                title: L10n.text("ui.host_monitor_io"),
                detail: "↓ \(ByteFormat.formattedRate(metrics.diskReadBPS))  ↑ \(ByteFormat.formattedRate(metrics.diskWriteBPS))",
                tokens: tokens
            )
            metricLine(
                icon: "network",
                title: L10n.text("ui.host_monitor_net"),
                detail: "↓ \(ByteFormat.formattedRate(metrics.networkRXBPS))  ↑ \(ByteFormat.formattedRate(metrics.networkTXBPS))",
                tokens: tokens
            )
        }
    }

    // MARK: - 推理引擎（Strata）

    @ViewBuilder
    private func strataSection(_ strata: HostStrataResponse, tokens: ThemeTokens) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            metricLine(
                icon: "sparkles",
                title: strata.model ?? L10n.text("ui.host_monitor_engine"),
                detail: stateDetail(strata),
                tokens: tokens
            )
            if let percent = strata.contextFillPercent {
                metricLine(
                    icon: "doc.text",
                    title: L10n.text("ui.host_monitor_context"),
                    detail: ByteFormat.formattedPercent(percent),
                    tokens: tokens
                )
                usageBar(fraction: fraction(percent, limit: 100), tokens: tokens)
            }
            if let tok = strata.tokPerSecond, tok > 0 {
                metricLine(
                    icon: "gauge",
                    title: L10n.text("ui.host_monitor_speed"),
                    detail: String(format: "%.1f t/s", tok),
                    tokens: tokens
                )
            }
            if let slots = strata.expertSlots, slots > 0 {
                metricLine(
                    icon: "square.stack.3d.up",
                    title: L10n.text("ui.host_monitor_experts"),
                    detail: expertDetail(strata),
                    tokens: tokens
                )
            }
        }
    }

    // MARK: - 行与条

    private func metricLine(
        icon: String,
        title: String,
        detail: String,
        tokens: ThemeTokens
    ) -> some View {
        HStack(spacing: 6) {
            Image(systemName: icon)
                .font(themeStore.uiFont(.caption2))
                .frame(width: 14)
            Text(title)
                .font(themeStore.uiFont(.caption2))
                .lineLimit(1)
            Spacer(minLength: 4)
            Text(detail)
                .font(themeStore.uiFont(.caption2, weight: .medium))
                .monospacedDigit()
                .lineLimit(1)
                .foregroundStyle(tokens.secondaryText)
        }
        .foregroundStyle(tokens.primaryText)
    }

    private func usageBar(fraction: Double, tokens: ThemeTokens) -> some View {
        GeometryReader { proxy in
            ZStack(alignment: .leading) {
                Capsule()
                    .fill(tokens.secondaryText.opacity(0.18))
                Capsule()
                    .fill(tokens.primaryText.opacity(0.75))
                    .frame(width: max(2, proxy.size.width * fraction))
            }
        }
        .frame(height: 3)
    }

    // MARK: - 文案

    private func gpuDetail(_ gpu: HostGPUMetrics) -> String {
        var parts: [String] = []
        if gpu.tempC > 0 {
            parts.append("\(Int(gpu.tempC.rounded()))°C")
        }
        if gpu.powerW > 0 {
            parts.append("\(Int(gpu.powerW.rounded())) W")
        }
        return parts.isEmpty ? ByteFormat.formattedPercent(gpu.utilPercent) : parts.joined(separator: " · ")
    }

    private func vramDetail(_ gpu: HostGPUMetrics) -> String {
        guard gpu.memTotalBytes > 0 else { return "--" }
        return "\(ByteFormat.formattedMemory(gpu.memUsedBytes)) / \(ByteFormat.formattedMemory(gpu.memTotalBytes))"
    }

    private func usageDetail(used: UInt64, total: UInt64) -> String {
        guard total > 0 else { return "--" }
        let percent = Double(used) / Double(total) * 100
        return "\(ByteFormat.formattedPercent(percent)) · \(ByteFormat.formattedMemory(used)) / \(ByteFormat.formattedMemory(total))"
    }

    private func stateDetail(_ strata: HostStrataResponse) -> String {
        if let tok = strata.tokPerSecond, tok > 0 {
            return String(format: "%.1f t/s", tok)
        }
        if let state = strata.state, !state.isEmpty {
            return state
        }
        return L10n.text("ui.host_monitor_idle")
    }

    private func expertDetail(_ strata: HostStrataResponse) -> String {
        var text = "\(strata.expertSlots ?? 0)"
        if let mib = strata.expertCacheMiB, mib > 0 {
            text += " · \(ByteFormat.formattedMemory(UInt64(mib) * 1024 * 1024))"
        }
        return text
    }

    private func fraction(used: UInt64, total: UInt64) -> Double {
        guard total > 0 else { return 0 }
        return min(1, max(0, Double(used) / Double(total)))
    }

    private func fraction(_ value: Double, limit: Double) -> Double {
        guard limit > 0 else { return 0 }
        return Swift.min(1, Swift.max(0, value / limit))
    }
}

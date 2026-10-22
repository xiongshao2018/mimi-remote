import SwiftUI
import WidgetKit

private enum MimiCarStatusWidgetConstants {
    static let kind = "MimiCarStatusWidget"
    static let fallbackRefreshInterval: TimeInterval = 15 * 60
}

private enum WidgetL10n {
    static func text(_ key: String) -> String {
        Bundle.main.localizedString(forKey: key, value: key, table: "Localizable")
    }

    static func format(_ key: String, _ arguments: String...) -> String {
        String(format: text(key), locale: .autoupdatingCurrent, arguments: arguments)
    }
}

private struct MimiCarStatusEntry: TimelineEntry {
    let date: Date
    let snapshot: CarStatusSnapshotV1?
}

private struct MimiCarStatusProvider: TimelineProvider {
    func placeholder(in context: Context) -> MimiCarStatusEntry {
        MimiCarStatusEntry(date: .now, snapshot: nil)
    }

    func getSnapshot(in context: Context, completion: @escaping (MimiCarStatusEntry) -> Void) {
        let snapshot = context.isPreview ? CarStatusSnapshotV1.widgetPreview() : CarStatusSnapshotStore.load()
        completion(MimiCarStatusEntry(date: .now, snapshot: snapshot))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<MimiCarStatusEntry>) -> Void) {
        let now = Date.now
        let snapshot = CarStatusSnapshotStore.load()
        var entries = [MimiCarStatusEntry(date: now, snapshot: snapshot)]

        // 预排一个 stale entry，让系统即使没收到主 App 的 reload 也能准时显示“可能已过期”。
        if let snapshot {
            let staleDate = snapshot.publishedAt.addingTimeInterval(CarStatusSnapshotV1.defaultStaleInterval)
            if staleDate > now {
                entries.append(MimiCarStatusEntry(date: staleDate, snapshot: snapshot))
            }
        }

        // Widget 不主动联网；兜底回读 App Group，真实变更仍由主 App 主动触发 timeline reload。
        let nextRefresh = (entries.last?.date ?? now)
            .addingTimeInterval(MimiCarStatusWidgetConstants.fallbackRefreshInterval)
        completion(Timeline(entries: entries, policy: .after(nextRefresh)))
    }
}

private struct MimiCarStatusWidgetView: View {
    let entry: MimiCarStatusEntry

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            identityRow
            Spacer(minLength: 8)
            content
            Spacer(minLength: 8)
            statusRow
        }
        .padding(14)
        .mimiWidgetContainerBackground {
            Color(.secondarySystemBackground)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(accessibilitySummary)
        // 父级 accessibilityLabel 同样包含项目和会话标题；整组标记敏感，
        // 避免锁屏 redaction 后 VoiceOver 仍从合并摘要朗读真实内容。
        .privacySensitive(entry.snapshot != nil)
    }

    private var identityRow: some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            Text("MIMI")
                .font(.caption2.weight(.bold))
                .tracking(0.8)
                .foregroundStyle(Color(red: 0.45, green: 0.24, blue: 0.88))
                .widgetAccentable()

            Spacer(minLength: 4)

            if let snapshot = entry.snapshot {
                Text(snapshot.activityDate, style: .relative)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        if let snapshot = entry.snapshot {
            VStack(alignment: .leading, spacing: 3) {
                Text(snapshot.projectDisplayName)
                    .font(.headline)
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                    .privacySensitive()

                Text(snapshot.sessionTitle)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                    .privacySensitive()
            }
        } else {
            VStack(alignment: .leading, spacing: 3) {
                Text(WidgetL10n.text("widget.car_status.empty_title"))
                    .font(.headline)
                    .foregroundStyle(.primary)
                Text(WidgetL10n.text("widget.car_status.empty_detail"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
        }
    }

    private var statusRow: some View {
        let presentation = statusPresentation
        return HStack(spacing: 6) {
            Circle()
                .fill(presentation.color)
                .frame(width: 7, height: 7)
                .accessibilityHidden(true)

            Text(presentation.label)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.primary)
                .lineLimit(1)
        }
    }

    private var statusPresentation: (label: String, color: Color) {
        guard let snapshot = entry.snapshot else {
            return (WidgetL10n.text("widget.car_status.awaiting_sync"), .secondary)
        }

        switch snapshot.effectiveDisplayStatus(
            at: entry.date,
            staleAfter: CarStatusSnapshotV1.defaultStaleInterval
        ) {
        case .running:
            return (WidgetL10n.text("widget.car_status.running"), .blue)
        case .needsAttention:
            return (WidgetL10n.text("widget.car_status.needs_attention"), .orange)
        case .completed:
            return (WidgetL10n.text("widget.car_status.completed"), .green)
        case .failed:
            return (WidgetL10n.text("widget.car_status.failed"), .red)
        case .offline:
            return (WidgetL10n.text("widget.car_status.offline"), .secondary)
        case .stale:
            return (WidgetL10n.text("widget.car_status.stale"), .orange)
        }
    }

    private var accessibilitySummary: String {
        let presentation = statusPresentation
        guard let snapshot = entry.snapshot else {
            return WidgetL10n.format(
                "widget.car_status.accessibility_empty",
                presentation.label
            )
        }
        return WidgetL10n.format(
            "widget.car_status.accessibility_value",
            snapshot.projectDisplayName,
            snapshot.sessionTitle,
            presentation.label
        )
    }
}

private struct MimiCarStatusWidget: Widget {
    let kind = MimiCarStatusWidgetConstants.kind

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: MimiCarStatusProvider()) { entry in
            MimiCarStatusWidgetView(entry: entry)
        }
        .configurationDisplayName(WidgetL10n.text("widget.car_status.name"))
        .description(WidgetL10n.text("widget.car_status.description"))
        .supportedFamilies([.systemSmall])
        // CarPlay 等系统表面可移除背景；内容不依赖卡片底色表达状态。
        // `containerBackgroundRemovable(_:)` 是 iOS 17 API，且系统默认就是可移除，
        // 下调部署目标后这里不再显式声明（见文件末尾的兼容说明）。
    }
}

@main
private struct MimiCarStatusWidgetBundle: WidgetBundle {
    var body: some Widget {
        MimiCarStatusWidget()
        MimiTurnLiveActivityWidget()
    }
}

// Widget 预览只有 `Preview(_:as:widget:timeline:)` 这一个宏：Xcode 26.5 SDK 实测写成
// `#Preview(as:) { ... }` 会报 “missing argument for parameter 'timeline'”，
// 补上 timeline 又报 “'Preview(_:as:widget:timeline:)' is only available in iOS 17.0 or newer”，
// 也就是部署目标 16.2 下这两种写法都编不过。预览只在 Xcode Canvas 里用，不进产物，
// 因此 iOS 16 构建（TrollStore 工作流注入 MIMI_LEGACY_IOS）整段跳过，
// iOS 17+ 的日常开发仍保留新鲜/过期两条样例时间线（样例数据见下面的 `widgetPreview(publishedAt:)`）。
#if !MIMI_LEGACY_IOS
#Preview(as: .systemSmall) {
    MimiCarStatusWidget()
} timeline: {
    MimiCarStatusEntry(date: .now, snapshot: .widgetPreview())
    MimiCarStatusEntry(
        date: .now,
        snapshot: .widgetPreview(
            publishedAt: .now.addingTimeInterval(-CarStatusSnapshotV1.defaultStaleInterval)
        )
    )
}
#endif

private extension CarStatusSnapshotV1 {
    static func widgetPreview(publishedAt: Date = .now) -> CarStatusSnapshotV1 {
        CarStatusSnapshotV1(
            profileID: "preview-profile",
            sessionID: "preview-session",
            projectDisplayName: "codex-ipad-agent",
            sessionTitle: "修复远程会话状态",
            displayStatus: .running,
            activityDate: .now.addingTimeInterval(-2 * 60),
            publishedAt: publishedAt
        )
    }
}

// MARK: - iOS 16.2 兼容（Widget 扩展独立编译，不能引用 App 里的 Core/Compat）

extension View {
    /// `containerBackground(for: .widget)` 是 iOS 17；iOS 16 的 Widget 没有容器背景概念，
    /// 底板由系统按样式给出，这里保持系统默认，不额外铺色。
    @ViewBuilder
    func mimiWidgetContainerBackground<Background: View>(
        @ViewBuilder _ content: () -> Background
    ) -> some View {
        if #available(iOS 17.0, *) {
            self.containerBackground(for: .widget, content: content)
        } else {
            self
        }
    }
}

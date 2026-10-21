import SwiftUI
import UniformTypeIdentifiers

struct LogPanelView: View {
    var body: some View {
        LogTailView()
    }
}

struct LogTailView: View {
    @EnvironmentObject private var appStore: AppStore
    @EnvironmentObject private var sessionStore: SessionStore
    @EnvironmentObject private var logStore: LogStore
    @EnvironmentObject private var themeStore: ThemeStore
    @Environment(\.colorScheme) private var colorScheme
    @State private var exportDocument: SessionLogExportDocument?
    @State private var exportFilename = L10n.text("ui.mimiremote_log_log")
    @State private var isPresentingExporter = false
    @State private var exportStatusMessage: String?
    @State private var exportErrorMessage: String?

    var body: some View {
        let tokens = themeStore.tokens(for: colorScheme)

        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Image(systemName: "terminal")
                    .font(themeStore.uiFont(.callout, weight: .semibold))
                    .foregroundStyle(tokens.secondaryText)
                    .frame(width: 24, height: 24)

                VStack(alignment: .leading, spacing: 4) {
                    Text(L10n.text("ui.log"))
                        .font(themeStore.uiFont(.subheadline, weight: .semibold))
                        .foregroundStyle(tokens.primaryText)
                    Text(sessionSubtitle)
                        .font(themeStore.codeFont(.caption2))
                        .foregroundStyle(tokens.secondaryText)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                .layoutPriority(1)

                Spacer()

                Button(action: prepareLogExport) {
                    Label(L10n.text("ui.export_log"), systemImage: "square.and.arrow.up")
                        .labelStyle(.titleAndIcon)
                }
                .buttonStyle(.borderless)
                .disabled(!canExportLog)
                .accessibilityLabel(L10n.text("ui.export_current_session_log"))
                .accessibilityHint(exportAccessibilityHint)

                Toggle(L10n.text("ui.autoscroll"), isOn: $logStore.autoScroll)
                    .toggleStyle(.switch)
                    .labelsHidden()
                    .accessibilityLabel(L10n.text("ui.autoscroll"))
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)

            if let exportStatusMessage {
                Text(exportStatusMessage)
                    .font(themeStore.uiFont(.caption2))
                    .foregroundStyle(tokens.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 12)
                    .padding(.bottom, 8)
                    .accessibilityLabel(exportStatusMessage)
            }

            Rectangle()
                .fill(tokens.border)
                .frame(height: 1)

            LogTailContentView()
        }
        .background(tokens.surface)
        .foregroundStyle(tokens.primaryText)
        .fileExporter(
            isPresented: $isPresentingExporter,
            document: exportDocument,
            contentType: SessionLogExportDocument.contentType,
            defaultFilename: exportFilename
        ) { result in
            switch result {
            case .success:
                exportStatusMessage = L10n.text("ui.log_exported_contains_only_current_cache_window")
            case .failure(let error):
                exportErrorMessage = L10n.format("ui.log_export_failed_value", error.localizedDescription)
            }
        }
        .alert(L10n.text("ui.unable_to_export_logs"), isPresented: exportErrorBinding) {
            Button(L10n.text("ui.got_it"), role: .cancel) {}
        } message: {
            Text(exportErrorMessage ?? L10n.text("ui.please_try_again_later"))
        }
    }

    private var sessionSubtitle: String {
        return sessionStore.selectedSessionID ?? L10n.text("ui.no_session_selected")
    }

    private var canExportLog: Bool {
        guard sessionStore.selectedSession != nil else { return false }
        return !logStore.cachedLogForExport(for: sessionStore.selectedSessionID)
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .isEmpty
    }

    private var exportAccessibilityHint: String {
        if sessionStore.selectedSession == nil {
            return L10n.text("ui.please_select_a_session_first")
        }
        if !canExportLog {
            return L10n.text("ui.there_are_no_exportable_cache_logs_for_the")
        }
        return L10n.text("ui.export_utf_8_logs_in_the_current_memory")
    }

    private var exportErrorBinding: Binding<Bool> {
        Binding(
            get: { exportErrorMessage != nil },
            set: { isPresented in
                if !isPresented {
                    exportErrorMessage = nil
                }
            }
        )
    }

    private func prepareLogExport() {
        guard let session = sessionStore.selectedSession,
              let payload = SessionLogExportBuilder.makePayload(
                  session: session,
                  cachedLog: logStore.cachedLogForExport(for: session.id),
                  generatedAt: Date(),
                  appVersion: SessionLogExportBuilder.currentAppVersion(),
                  macDisplayName: appStore.activeConnectionProfile?.displayName ?? L10n.text("ui.current_mac")
              ) else {
            return
        }
        exportDocument = SessionLogExportDocument(content: payload.content)
        exportFilename = payload.filename
        exportStatusMessage = nil
        isPresentingExporter = true
    }
}

struct SessionLogExportPayload: Equatable {
    let content: String
    let filename: String
}

enum SessionLogExportBuilder {
    static let maximumFilenameLength = 64
    static let maximumFilenameUTF8Bytes = 180

    static func makePayload(
        session: AgentSession?,
        cachedLog: String,
        generatedAt: Date,
        appVersion: String,
        macDisplayName: String
    ) -> SessionLogExportPayload? {
        guard let session,
              !cachedLog.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return nil
        }

        // 文件头严格使用白名单字段：不接收 AppStore、endpoint 或 Token，避免误把凭据写入诊断文件。
        let header = [
            L10n.format("ui.generation_time_value", ISO8601DateFormatter().string(from: generatedAt)),
            L10n.format("ui.app_version_value", singleLine(appVersion, fallback: L10n.text("ui.unknown"))),
            L10n.format("ui.session_id_value", singleLine(session.id, fallback: L10n.text("ui.unknown"))),
            L10n.format("ui.session_title_value", singleLine(session.title, fallback: L10n.text("ui.unnamed_session"))),
            L10n.format("ui.current_mac_named", singleLine(macDisplayName, fallback: L10n.text("ui.current_mac")))
        ].joined(separator: "\n")
        return SessionLogExportPayload(
            content: header + "\n\n" + cachedLog,
            filename: safeFilename(session: session, generatedAt: generatedAt)
        )
    }

    static func currentAppVersion(bundle: Bundle = .main) -> String {
        let version = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
        let build = bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String
        switch (version?.trimmingCharacters(in: .whitespacesAndNewlines), build?.trimmingCharacters(in: .whitespacesAndNewlines)) {
        case let (version?, build?) where !version.isEmpty && !build.isEmpty:
            return "\(version) (\(build))"
        case let (version?, _) where !version.isEmpty:
            return version
        case let (_, build?) where !build.isEmpty:
            return build
        default:
            return L10n.text("ui.unknown")
        }
    }

    static func safeFilename(session: AgentSession, generatedAt: Date) -> String {
        let rawTitle = session.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            ? session.id
            : session.title
        let safeTitle = safeFilenameComponent(rawTitle)
        let timestamp = filenameTimestamp(generatedAt)
        let prefix = "MimiRemote-"
        let suffix = "-\(timestamp).log"
        let availableTitleLength = max(1, maximumFilenameLength - prefix.count - suffix.count)
        let availableTitleBytes = max(1, maximumFilenameUTF8Bytes - prefix.utf8.count - suffix.utf8.count)
        let boundedTitle = boundedFilenameComponent(
            safeTitle,
            maximumCharacters: availableTitleLength,
            maximumUTF8Bytes: availableTitleBytes
        )
        return prefix + boundedTitle + suffix
    }

    private static func boundedFilenameComponent(
        _ value: String,
        maximumCharacters: Int,
        maximumUTF8Bytes: Int
    ) -> String {
        var result = ""
        for character in value {
            let candidate = result + String(character)
            guard candidate.count <= maximumCharacters,
                  candidate.utf8.count <= maximumUTF8Bytes else {
                break
            }
            result = candidate
        }
        // 极端组合字符可能单个字形就超过字节预算，使用稳定的 ASCII 回退值。
        return result.isEmpty ? "session" : result
    }

    private static func safeFilenameComponent(_ raw: String) -> String {
        let forbidden = CharacterSet(charactersIn: "/\\:?%*|\"<>.")
            .union(.controlCharacters)
            .union(.newlines)
        var result = ""
        var previousWasSeparator = false
        for scalar in raw.unicodeScalars {
            let shouldReplace = forbidden.contains(scalar) || CharacterSet.whitespaces.contains(scalar)
            if shouldReplace {
                if !previousWasSeparator, !result.isEmpty {
                    result.append("-")
                }
                previousWasSeparator = true
            } else {
                result.unicodeScalars.append(scalar)
                previousWasSeparator = false
            }
        }
        let trimmed = result.trimmingCharacters(in: CharacterSet(charactersIn: "-_"))
        return trimmed.isEmpty ? "session" : trimmed
    }

    private static func singleLine(_ raw: String, fallback: String) -> String {
        var sanitized = ""
        for scalar in raw.components(separatedBy: .newlines).joined(separator: " ").unicodeScalars
        where !CharacterSet.controlCharacters.contains(scalar) {
            sanitized.unicodeScalars.append(scalar)
        }
        let normalized = sanitized.trimmingCharacters(in: .whitespacesAndNewlines)
        return normalized.isEmpty ? fallback : String(normalized.prefix(240))
    }

    private static func filenameTimestamp(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyyMMdd-HHmmss"
        return formatter.string(from: date)
    }
}

struct SessionLogExportDocument: FileDocument {
    static let contentType = UTType(filenameExtension: "log", conformingTo: .plainText) ?? .plainText
    static var readableContentTypes: [UTType] { [contentType] }

    private let data: Data

    init(content: String) {
        data = Data(content.utf8)
    }

    init(configuration: ReadConfiguration) throws {
        data = configuration.file.regularFileContents ?? Data()
    }

    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper {
        FileWrapper(regularFileWithContents: data)
    }
}

struct LogTailContentView: View {
    @EnvironmentObject private var sessionStore: SessionStore
    @EnvironmentObject private var logStore: LogStore
    @EnvironmentObject private var themeStore: ThemeStore
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        // 行已在 LogStore 后台算好，这里只读缓存，body 不再做重活。
        let lines = logStore.lines(for: sessionStore.selectedSessionID)
        return ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 6) {
                    if lines.isEmpty {
                        CompatContentUnavailableView(
                            L10n.text("ui.no_logs_yet"),
                            systemImage: "terminal",
                            description: Text(L10n.text("ui.there_is_no_terminal_output_for_the_current"))
                        )
                        .font(themeStore.uiFont(.caption))
                        .padding(.top, 48)
                    } else {
                        ForEach(lines) { line in
                            LogLineRow(line: line)
                        }
                    }
                    Color.clear
                        .frame(height: 1)
                        .id("bottom")
                }
                .frame(maxWidth: .infinity, alignment: .topLeading)
                .padding(8)
            }
            .background(themeStore.tokens(for: colorScheme).background)
            .onChange(of: lines.count) {_ in
                scrollToBottom(proxy)
            }
            .onChange(of: lines.last?.text) {_ in
                scrollToBottom(proxy)
            }
        }
    }

    private func scrollToBottom(_ proxy: ScrollViewProxy) {
        guard logStore.autoScroll else {
            return
        }
        proxy.scrollTo("bottom", anchor: .bottom)
    }
}

struct LogDisplayLine: Identifiable, Hashable {
    enum Kind: Hashable {
        case command
        case assistant
        case system
        case warning
        case plain

        var symbolName: String {
            switch self {
            case .command:
                return "chevron.right"
            case .assistant:
                return "text.bubble"
            case .system:
                return "gearshape"
            case .warning:
                return "exclamationmark.triangle"
            case .plain:
                return "terminal"
            }
        }
    }

    let id: Int
    let text: String
    let kind: Kind
}

struct LogPanelFormatter {
    private let maxRenderedLogLines = 360

    func renderedLines(from log: String, startLineID: Int = 0) -> [LogDisplayLine] {
        guard !log.isEmpty else {
            return []
        }

        // 只渲染最新的可见行，同时把日志重绘产生的大量空行和边框压掉。
        let normalizedLines = log
            .replacingOccurrences(of: "\r", with: "\n")
            .split(separator: "\n", omittingEmptySubsequences: false)
            .map { normalizeTerminalLine(String($0)) }

        var result: [LogDisplayLine] = []
        var lastKey = ""
        for (rawIndex, rawLine) in normalizedLines.enumerated() {
            guard let line = makeDisplayLine(from: rawLine, id: startLineID + rawIndex) else {
                continue
            }
            // 按归一化后的语义文本去重：日志重绘常常只差尾部输入框占位符或空白，
            // 原来的“严格相邻相等”挡不住，这里用压缩后的 key 把这些近似重复行合并掉。
            let dedupKey = dedupKey(for: line)
            let effectiveKey = dedupKey.isEmpty ? line.text : dedupKey
            guard effectiveKey != lastKey else {
                continue
            }
            result.append(line)
            lastKey = effectiveKey
        }
        return Array(result.suffix(maxRenderedLogLines))
    }

    private func makeDisplayLine(from line: String, id: Int) -> LogDisplayLine? {
        let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !isNoiseLine(trimmed) else {
            return nil
        }

        if trimmed.hasPrefix("[agentd] warning") || trimmed.hasPrefix("warning:") {
            return LogDisplayLine(id: id, text: trimmed, kind: .warning)
        }
        if trimmed.hasPrefix("[agentd]") {
            return LogDisplayLine(id: id, text: trimmed, kind: .system)
        }
        if trimmed.hasPrefix("›") || trimmed.hasPrefix(">") {
            return LogDisplayLine(id: id, text: stripPromptPrefix(trimmed), kind: .command)
        }
        if trimmed.hasPrefix("•") || trimmed.hasPrefix("●") {
            return LogDisplayLine(id: id, text: cleanAssistantText(stripBulletPrefix(trimmed)), kind: .assistant)
        }
        // 普通日志行只做“无损”的重复句子折叠，绝不按 prompt 片段截断，
        // 否则像 "Home › Settings"、"note: > Implement later"、"data: • item" 这类正常输出会被误伤。
        return LogDisplayLine(id: id, text: collapseRepeatedSentences(trimmed), kind: .plain)
    }

    private func dedupKey(for line: LogDisplayLine) -> String {
        switch line.kind {
        case .assistant:
            // assistant/bullet 行是 prompt 残片的高发区，按剥离 prompt 片段后的语义文本去重。
            return AssistantTextNormalizer.normalizedAssistantTextForDedup(line.text)
        default:
            // 其余行（plain/command/system…）只折叠重复句子 + 去空白，不截断含 "›"/">" 的正常内容。
            return AssistantTextNormalizer.plainDedupKey(line.text)
        }
    }

    private func cleanAssistantText(_ text: String) -> String {
        // assistant 气泡行：1) 去掉被日志重绘拼到行尾的输入框占位符（"… ›Implement {feature} …"）；
        // 2) 合并同一行里被重画两遍的句子。失败时回退原文，避免把正常内容清空。
        let stripped = AssistantTextNormalizer.stripTerminalPromptFragment(text, dropPromptOnlyLine: false)
        let collapsed = AssistantTextNormalizer
            .collapseAdjacentRepeatedSentenceSegments(stripped)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return collapsed.isEmpty ? text : collapsed
    }

    private func collapseRepeatedSentences(_ text: String) -> String {
        let collapsed = AssistantTextNormalizer
            .collapseAdjacentRepeatedSentenceSegments(text)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return collapsed.isEmpty ? text : collapsed
    }

    private func normalizeTerminalLine(_ line: String) -> String {
        let tableChars = CharacterSet(charactersIn: "╭╮╰╯│─┌┐└┘├┤┬┴┼")
        let withoutChrome = line
            .components(separatedBy: tableChars)
            .joined(separator: " ")
        return withoutChrome
            .replacingOccurrences(of: #"\s{2,}"#, with: " ", options: .regularExpression)
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func stripPromptPrefix(_ line: String) -> String {
        String(line.drop { $0 == "›" || $0 == ">" || $0 == " " })
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func stripBulletPrefix(_ line: String) -> String {
        String(line.drop { $0 == "•" || $0 == "●" || $0 == " " })
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func isNoiseLine(_ line: String) -> Bool {
        if line == "Working" || line == "thinking" || line == "esc to interrupt" {
            return true
        }
        // Streaming redraws can leave partial W/Wo/Wor status tokens in the log.
        if "Working".hasPrefix(line), line.count <= 6 {
            return true
        }
        if line.count <= 2, line.unicodeScalars.allSatisfy({ CharacterSet.decimalDigits.contains($0) }) {
            return true
        }
        return false
    }
}

private struct LogLineRow: View {
    @EnvironmentObject private var themeStore: ThemeStore
    @Environment(\.colorScheme) private var colorScheme
    let line: LogDisplayLine

    var body: some View {
        let tokens = themeStore.tokens(for: colorScheme)

        HStack(alignment: .top, spacing: 7) {
            Image(systemName: line.kind.symbolName)
                .font(themeStore.uiFont(.caption2, weight: .semibold))
                .foregroundStyle(rowTint(tokens: tokens))
                .frame(width: 13, height: 16)
                .padding(.top, 1)

            Text(line.text)
                .font(themeStore.codeFont(size: 11))
                .foregroundStyle(tokens.primaryText)
                .lineLimit(3)
                .truncationMode(.tail)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 7)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(rowBackground(tokens: tokens))
        .clipShape(RoundedRectangle(cornerRadius: 7, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: 7, style: .continuous)
                .strokeBorder(rowBorder(tokens: tokens), lineWidth: 1)
        }
    }

    private func rowTint(tokens: ThemeTokens) -> Color {
        switch line.kind {
        case .command:
            return tokens.accent
        case .assistant:
            return tokens.success
        case .system, .warning:
            return tokens.warning
        case .plain:
            return tokens.secondaryText
        }
    }

    private func rowBackground(tokens: ThemeTokens) -> Color {
        switch line.kind {
        case .command:
            return tokens.accent.opacity(0.10)
        case .assistant:
            return tokens.success.opacity(0.08)
        case .system:
            return tokens.warning.opacity(0.10)
        case .warning:
            return tokens.warning.opacity(0.12)
        case .plain:
            return tokens.elevatedSurface
        }
    }

    private func rowBorder(tokens: ThemeTokens) -> Color {
        switch line.kind {
        case .command:
            return tokens.accent.opacity(0.18)
        case .assistant:
            return tokens.success.opacity(0.16)
        case .system, .warning:
            return tokens.warning.opacity(0.20)
        case .plain:
            return tokens.border.opacity(0.65)
        }
    }
}

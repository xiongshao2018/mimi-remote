import SwiftUI
import UIKit

// iOS 16.2 兼容层（#iOS16 适配）。
//
// 本 App 的部署目标从 iOS 18 下调到 iOS 16.2，以支持 iOS 16.3.1 真机（TrollStore 自签安装）。
// 所有 iOS 17/18 才有的 API 统一收口在这里，以 `compat*` 修饰符提供：
// 高版本走原生实现保持现有体验，低版本按「视觉降级可接受、功能链路必须保留」的原则降级。
// 滚动几何 / 相位 / 可见性观测见同目录 `CompatScrollGeometry.swift`。

// MARK: - 无障碍播报（AccessibilityNotification.Announcement 是 iOS 17）

enum CompatAccessibility {
    @MainActor
    static func announce(_ message: String) {
        if #available(iOS 17.0, *) {
            AccessibilityNotification.Announcement(message).post()
        } else {
            UIAccessibility.post(notification: .announcement, argument: message)
        }
    }
}

// MARK: - 比部署目标新的类型不能出现在参数位置

/// `ScrollBounceBehavior` 本身是 iOS 16.4，直接当参数写在 16.2 的签名里就编不过。
/// 兼容层用自己的枚举收口，case 名与系统一致，调用点写法不变。
enum CompatScrollBounceBehavior {
    case basedOnSize
    case always

    @available(iOS 16.4, *)
    var native: ScrollBounceBehavior {
        switch self {
        case .basedOnSize: return .basedOnSize
        case .always: return .always
        }
    }
}

/// `ScrollAnchorBehavior` 是 iOS 18，同理收口。
enum CompatScrollAnchorBehavior {
    case initialOffset
    case alignment

    @available(iOS 18.0, *)
    var native: ScrollAnchorBehavior {
        switch self {
        case .initialOffset: return .initialOffset
        case .alignment: return .alignment
        }
    }
}

// MARK: - 小修饰符：低版本 no-op 或原生转发

extension View {
    /// `scrollBounceBehavior` 是 iOS 16.4；16.2/16.3 上保持系统默认回弹。
    @ViewBuilder
    func compatScrollBounceBehavior(
        _ behavior: CompatScrollBounceBehavior,
        axes: Axis.Set = .vertical
    ) -> some View {
        if #available(iOS 16.4, *) {
            self.scrollBounceBehavior(behavior.native, axes: axes)
        } else {
            self
        }
    }

    /// `presentationCornerRadius` 是 iOS 16.4；低版本用系统默认圆角。
    @ViewBuilder
    func compatPresentationCornerRadius(_ radius: CGFloat) -> some View {
        if #available(iOS 16.4, *) {
            self.presentationCornerRadius(radius)
        } else {
            self
        }
    }

    /// `listSectionSpacing` 是 iOS 17；低版本用系统默认段距。
    @ViewBuilder
    func compatListSectionSpacing(_ spacing: CGFloat) -> some View {
        if #available(iOS 17.0, *) {
            self.listSectionSpacing(spacing)
        } else {
            self
        }
    }

    /// `contentTransition` 修饰符是 iOS 16；16.2 起可用，保留原生转发便于以后统一入口。
    @ViewBuilder
    func compatContentTransition(_ transition: ContentTransition) -> some View {
        if #available(iOS 16.0, *) {
            self.contentTransition(transition)
        } else {
            self
        }
    }

    /// `defaultScrollAnchor(_:for:)` 的两参形态是 iOS 18；低版本由各自的初始定位逻辑兜底。
    @ViewBuilder
    func compatDefaultScrollAnchor(
        _ anchor: UnitPoint,
        for behavior: CompatScrollAnchorBehavior
    ) -> some View {
        if #available(iOS 18.0, *) {
            self.defaultScrollAnchor(anchor, for: behavior.native)
        } else {
            self
        }
    }

    /// `symbolEffect(.appear)` 是 iOS 17；低版本图标直接出现，无入场动画。
    @ViewBuilder
    func compatSymbolAppear() -> some View {
        if #available(iOS 17.0, *) {
            self.symbolEffect(.appear, options: .nonRepeating)
        } else {
            self
        }
    }

    /// SwiftUI `focusable()` 是 iOS 17；低版本没有列表键盘焦点宿主，方向键快捷键整体不可用。
    @ViewBuilder
    func compatFocusableForListKeyboard() -> some View {
        if #available(iOS 17.0, *) {
            self.focusable()
        } else {
            self
        }
    }

    /// Widget 容器背景是 iOS 17；低版本 Widget 用系统默认样式。
    @ViewBuilder
    func compatWidgetContainerBackground<Background: View>(
        @ViewBuilder _ content: () -> Background
    ) -> some View {
        if #available(iOS 17.0, *) {
            self.containerBackground(for: .widget, content: content)
        } else {
            self
        }
    }

    /// `containerBackgroundRemovable` 是 iOS 17；低版本保持系统默认。
    @ViewBuilder
    func compatContainerBackgroundRemovable(_ removable: Bool) -> some View {
        if #available(iOS 17.0, *) {
            self.containerBackgroundRemovable(removable)
        } else {
            self
        }
    }
}

// MARK: - 键盘方向键（onKeyPress / KeyEquivalent 是 iOS 17）

enum CompatKeyPressKey {
    case upArrow
    case downArrow
    case returnKey
}

extension View {
    /// 低版本上方向键导航整体退化为不可用；回车提交仍走 `onSubmit`。
    @ViewBuilder
    func compatOnArrowKeyPress(action: @escaping (CompatKeyPressKey) -> Bool) -> some View {
        if #available(iOS 17.0, *) {
            self
                .onKeyPress(.upArrow) { action(.upArrow) ? .handled : .ignored }
                .onKeyPress(.downArrow) { action(.downArrow) ? .handled : .ignored }
        } else {
            self
        }
    }

    /// 会话列表需要方向键 + 回车；低版本同样退化。
    @ViewBuilder
    func compatOnArrowAndReturnKeyPress(action: @escaping (CompatKeyPressKey) -> Bool) -> some View {
        if #available(iOS 17.0, *) {
            self
                .onKeyPress(keys: [.upArrow, .downArrow, .return]) { keyPress in
                    switch keyPress.key {
                    case .upArrow:
                        return action(.upArrow) ? .handled : .ignored
                    case .downArrow:
                        return action(.downArrow) ? .handled : .ignored
                    case .return:
                        return action(.returnKey) ? .handled : .ignored
                    default:
                        return .ignored
                    }
                }
        } else {
            self
        }
    }
}

// MARK: - onGeometryChange（iOS 18）：低版本用背景 GeometryReader + PreferenceKey 复刻

private struct CompatGeometryBox<V> {
    let value: V
}

private struct CompatGeometryKey<V: Equatable>: PreferenceKey {
    static var defaultValue: CompatGeometryBox<V>? { nil }
    static func reduce(value: inout CompatGeometryBox<V>?, nextValue: () -> CompatGeometryBox<V>?) {
        value = nextValue() ?? value
    }
}

private struct CompatGeometryObserver<V: Equatable>: View {
    let transform: (GeometryProxy) -> V
    let action: (V) -> Void

    var body: some View {
        GeometryReader { proxy in
            Color.clear
                .preference(key: CompatGeometryKey<V>.self, value: CompatGeometryBox(value: transform(proxy)))
        }
        .frame(width: 0, height: 0)
        .onPreferenceChange(CompatGeometryKey<V>.self) { box in
            guard let box else { return }
            action(box.value)
        }
    }
}

extension View {
    /// iOS 18 原生 `onGeometryChange`；低版本用不参与布局的背景观测器复刻语义。
    @ViewBuilder
    func compatOnGeometryChange<V: Equatable>(
        for type: V.Type,
        of transform: @escaping (GeometryProxy) -> V,
        action: @escaping (V) -> Void
    ) -> some View {
        if #available(iOS 18.0, *) {
            self.onGeometryChange(for: type, of: transform, action: action)
        } else {
            self.background(CompatGeometryObserver(transform: transform, action: action))
        }
    }
}

// MARK: - ContentUnavailableView（iOS 17）：低版本用等价的居中占位布局复刻

/// 与 `ContentUnavailableView` 三种构造形态同签名的兼容视图。
/// 刻意不写死字号：图标用 `imageScale(.large)`、文本继承环境字体，
/// 让调用点上的 `.font(...)` / `.foregroundStyle(...)` 与系统实现一样生效。
struct CompatContentUnavailableView<Label: View, Description: View, Actions: View>: View {
    private let label: Label
    private let description: Description
    private let actions: Actions
    private let stacksIconAboveTitle: Bool

    init(
        @ViewBuilder label: () -> Label,
        @ViewBuilder description: () -> Description,
        @ViewBuilder actions: () -> Actions
    ) {
        self.label = label()
        self.description = description()
        self.actions = actions()
        self.stacksIconAboveTitle = false
    }

    private init(
        icon: String,
        title: String,
        description: Description,
        stacksIconAboveTitle: Bool
    ) where Label == AnyView, Actions == EmptyView {
        self.label = AnyView(
            Group {
                if stacksIconAboveTitle {
                    VStack(spacing: 10) {
                        Image(systemName: icon).imageScale(.large)
                        Text(title)
                    }
                } else {
                    Label(title, systemImage: icon)
                }
            }
        )
        self.description = description
        self.actions = EmptyView()
        self.stacksIconAboveTitle = stacksIconAboveTitle
    }
}

extension CompatContentUnavailableView where Description == EmptyView, Actions == EmptyView {
    init(_ title: some StringProtocol, systemImage: String) where Label == AnyView {
        self.init(icon: systemImage, title: String(title), description: EmptyView(), stacksIconAboveTitle: true)
    }
}

extension CompatContentUnavailableView where Actions == EmptyView, Label == AnyView {
    init(_ title: some StringProtocol, systemImage: String, description: Description) {
        self.init(icon: systemImage, title: String(title), description: description, stacksIconAboveTitle: true)
    }
}

extension CompatContentUnavailableView {
    var body: some View {
        VStack(spacing: 12) {
            label
            if !(description is EmptyView) {
                description
                    .multilineTextAlignment(.center)
            }
            if !(actions is EmptyView) {
                actions
            }
        }
        .foregroundStyle(.secondary)
        .frame(maxWidth: .infinity)
        .padding(.vertical, 24)
    }
}

// MARK: - onChange(of:initial:_:) 的两参数闭包（iOS 17）

/// iOS 17 把 `onChange` 的闭包换成 `(oldValue, newValue)`，并新增 `initial:` 参数；
/// iOS 16 只有 `onChange(of:perform:)`（单参数、无 `initial:`）。这里用 `@State`
/// 缓存上一轮的值，在 iOS 16 上还原「首次可立即回调 + 旧值可用」的语义，调用点写法统一。
///
/// 旧值来自视图首次出现时记录的快照（与原生 `initial: true` 一样，首轮回调的旧值等于新值）；
/// 只有「视图还没出现值就变了」这一种极端时序会丢掉真实旧值，本仓库的调用点都不依赖它。
private struct CompatOnChangeModifier<Value: Equatable>: ViewModifier {
    let value: Value
    let initial: Bool
    let action: (Value, Value) -> Void

    @State private var previousValue: Value?
    @State private var didFireInitial = false

    func body(content: Content) -> some View {
        content
            .onAppear {
                if initial, !didFireInitial {
                    didFireInitial = true
                    action(value, value)
                }
                previousValue = value
            }
            .onChange(of: value) { newValue in
                action(previousValue ?? newValue, newValue)
                previousValue = newValue
            }
    }
}

extension View {
    /// 单参数形态：只关心新值，`initial: true` 表示首次出现时立即执行一次。
    @ViewBuilder
    func compatOnChange<Value: Equatable>(
        of value: Value,
        initial: Bool = false,
        _ action: @escaping (Value) -> Void
    ) -> some View {
        modifier(
            CompatOnChangeModifier(value: value, initial: initial) { _, newValue in
                action(newValue)
            }
        )
    }

    /// 双参数形态：需要旧值参与判断时用（iOS 16 上旧值由兼容层缓存）。
    @ViewBuilder
    func compatOnChangeWithPrevious<Value: Equatable>(
        of value: Value,
        initial: Bool = false,
        _ action: @escaping (Value, Value) -> Void
    ) -> some View {
        modifier(CompatOnChangeModifier(value: value, initial: initial, action: action))
    }
}

// MARK: - contentMargins（iOS 17）

/// `ContentMarginPlacement` 本身是 iOS 17，不能直接出现在 16.2 的参数位置。
/// case 名与系统一致，调用点只换修饰符名。
enum CompatContentMarginPlacement: Equatable {
    case scrollContent
    case scrollIndicators
    case contentLayout

    @available(iOS 17.0, *)
    var native: ContentMarginPlacement {
        switch self {
        case .scrollContent: return .scrollContent
        case .scrollIndicators: return .scrollIndicators
        case .contentLayout: return .contentLayout
        }
    }
}

/// iOS 16 没有 `contentMargins`。用 `safeAreaInset` 的透明占位造出等高的滚动内容内边距：
/// 列表视口与背景仍铺满，只有内容多让一段，最后一条也能滚到浮层之上被读到。
private struct CompatEdgeSpacerModifier: ViewModifier {
    let edge: Edge?
    let length: CGFloat

    @ViewBuilder
    func body(content: Content) -> some View {
        if let edge, length > 0 {
            switch edge {
            case .top:
                content.safeAreaInset(edge: .top, spacing: 0) { Color.clear.frame(height: length) }
            case .bottom:
                content.safeAreaInset(edge: .bottom, spacing: 0) { Color.clear.frame(height: length) }
            case .leading:
                content.safeAreaInset(edge: .leading, spacing: 0) { Color.clear.frame(width: length) }
            case .trailing:
                content.safeAreaInset(edge: .trailing, spacing: 0) { Color.clear.frame(width: length) }
            @unknown default:
                content
            }
        } else {
            content
        }
    }
}

extension View {
    @ViewBuilder
    func compatContentMargins(
        _ edges: Edge.Set = .all,
        _ length: CGFloat,
        for placement: CompatContentMarginPlacement = .contentLayout
    ) -> some View {
        if #available(iOS 17.0, *) {
            self.contentMargins(edges, length, for: placement.native)
        } else if placement == .scrollContent, length > 0 {
            modifier(CompatEdgeSpacerModifier(edge: edges.contains(.top) ? .top : nil, length: length))
                .modifier(CompatEdgeSpacerModifier(edge: edges.contains(.bottom) ? .bottom : nil, length: length))
                .modifier(CompatEdgeSpacerModifier(edge: edges.contains(.leading) ? .leading : nil, length: length))
                .modifier(CompatEdgeSpacerModifier(edge: edges.contains(.trailing) ? .trailing : nil, length: length))
        } else {
            self
        }
    }
}

// MARK: - presentationBackground（iOS 16.4）

extension View {
    /// 16.2 没有 Sheet 背景自定义：退化为系统默认底板，只丢配色不丢功能。
    @ViewBuilder
    func compatPresentationBackground<S: ShapeStyle>(_ style: S) -> some View {
        if #available(iOS 16.4, *) {
            self.presentationBackground(style)
        } else {
            self
        }
    }

    /// 闭包形态：调用方给 `AnyShapeStyle`，View 当样式用（iOS 17）的部分由调用点自己门控。
    @ViewBuilder
    func compatPresentationBackgroundStyle(_ style: () -> AnyShapeStyle) -> some View {
        if #available(iOS 16.4, *) {
            self.presentationBackground(style())
        } else {
            self
        }
    }
}

// MARK: - presentationSizing（iOS 18）

enum CompatPresentationSizing {
    case form
    case fitted
}

extension View {
    /// iOS 16 没有 Sheet 尺寸策略 API，交给系统默认（表单按内容自适应）。
    @ViewBuilder
    func compatPresentationSizing(_ sizing: CompatPresentationSizing) -> some View {
        if #available(iOS 18.0, *) {
            switch sizing {
            case .form: self.presentationSizing(.form)
            case .fitted: self.presentationSizing(.fitted)
            }
        } else {
            self
        }
    }
}

// MARK: - navigationTransition(.zoom)（iOS 18）

extension View {
    /// 16.2 没有匹配式展开动画：Sheet 直接按系统默认方式出现。
    @ViewBuilder
    func compatZoomTransition(sourceID: String, in namespace: Namespace.ID) -> some View {
        if #available(iOS 18.0, *) {
            self.navigationTransition(.zoom(sourceID: sourceID, in: namespace))
        } else {
            self
        }
    }
}

// MARK: - 动画预设（Animation.smooth / .snappy / .bounce 是 iOS 17）

extension Animation {
    /// iOS 17 的 `.snappy(duration:)`：短、几乎不回弹的弹簧。
    /// iOS 16.2 没有该预设，用同参数的 `spring(response:dampingFraction:)` 近似：
    /// response 取时长的一半、阻尼接近临界，观感同样是快速到位且不来回晃。
    static func compatSnappy(duration: Double) -> Animation {
        if #available(iOS 17.0, *) {
            return .snappy(duration: duration)
        }
        return .spring(
            response: Swift.max(0.05, duration * 0.55),
            dampingFraction: 0.9,
            blendDuration: 0
        )
    }
}

// MARK: - toolbarBackgroundVisibility（iOS 18）

extension View {
    /// `toolbarBackgroundVisibility(_:for:)` 是 iOS 18。
    /// iOS 16.2 用同期的 `toolbarBackground(_:for:)` Visibility 重载表达同一意图，
    /// 语义一致（控制 bar 底板显隐），只是 iOS 27 起那个写法被标记废弃。
    @ViewBuilder
    func compatToolbarBackgroundVisibility(
        _ visibility: Visibility,
        for bars: ToolbarPlacement...
    ) -> some View {
        if #available(iOS 18.0, *) {
            self.toolbarBackgroundVisibility(visibility, for: bars)
        } else {
            self.toolbarBackground(visibility, for: bars)
        }
    }
}

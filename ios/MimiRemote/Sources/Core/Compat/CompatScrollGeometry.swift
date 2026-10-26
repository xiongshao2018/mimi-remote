import SwiftUI
import UIKit

// iOS 16 的 SwiftUI 没有 `onScrollGeometryChange` / `onScrollPhaseChange` /
// `onScrollVisibilityChange`（iOS 18+，iOS 17 同样没有）。本文件用 UIKit 内省补齐：从宿主视图向上找到
// 承载它的 UIScrollView（与 `ConversationTimelineScrollViewLocator` 同一策略），
// 用 KVO 观测 contentOffset / contentSize / adjustedContentInset / isDragging /
// isDecelerating，向调用点发出与原生语义对齐的快照。iOS 18 及以上仍走原生实现。

// MARK: - 快照类型

/// 与调用点读取的 `ScrollGeometry` 字段对齐的滚动几何快照。
struct CompatScrollGeometry {
    let contentOffset: CGPoint
    let contentInsets: EdgeInsets
    let contentSize: CGSize
    let containerSize: CGSize

    /// 内容坐标系下的可见区域，语义与 `ScrollGeometry.visibleRect` 对齐。
    var visibleRect: CGRect {
        CGRect(origin: contentOffset, size: containerSize)
    }

    @available(iOS 18.0, *)
    init(_ geometry: ScrollGeometry) {
        contentOffset = geometry.contentOffset
        contentInsets = geometry.contentInsets
        contentSize = geometry.contentSize
        containerSize = geometry.containerSize
    }

    fileprivate init(_ scrollView: UIScrollView) {
        let insets = scrollView.adjustedContentInset
        contentOffset = scrollView.contentOffset
        contentInsets = EdgeInsets(
            top: insets.top,
            leading: insets.left,
            bottom: insets.bottom,
            trailing: insets.right
        )
        contentSize = scrollView.contentSize
        containerSize = scrollView.bounds.size
    }
}

/// 与 `ScrollPhase` 对齐的滚动相位。低版本没有 `.animating` 与 tracking/interacting
/// 的细分：手指按住统一报 `.interacting`，惯性统一报 `.decelerating`。
enum CompatScrollPhase: Equatable {
    case idle
    case tracking
    case interacting
    case decelerating
    case animating

    var isUserDriven: Bool {
        switch self {
        case .tracking, .interacting, .decelerating:
            return true
        case .idle, .animating:
            return false
        }
    }

    @available(iOS 18.0, *)
    init(_ phase: ScrollPhase) {
        switch phase {
        case .idle:
            self = .idle
        case .tracking:
            self = .tracking
        case .interacting:
            self = .interacting
        case .decelerating:
            self = .decelerating
        case .animating:
            self = .animating
        @unknown default:
            self = .idle
        }
    }

    fileprivate static func current(_ scrollView: UIScrollView) -> CompatScrollPhase {
        if scrollView.isDragging {
            return .interacting
        }
        if scrollView.isDecelerating {
            return .decelerating
        }
        return .idle
    }
}

// MARK: - 观测器

private final class CompatScrollObservationView: UIView {
    var onGeometry: ((CompatScrollGeometry) -> Void)?
    var onPhase: ((CompatScrollPhase) -> Void)?
    var onVisibility: ((Bool) -> Void)?
    var visibilityThreshold: CGFloat = 0.01

    /// 采样值形态：与原生 `onScrollGeometryChange(for:)` 一样，只在采样值变化时回调。
    /// 类型擦除后由调用点提供的等值判断去重，避免每次滚动都写一次 @State。
    var geometryValueSample: ((CompatScrollGeometry) -> Any)?
    var geometryValueIsEqual: ((Any, Any) -> Bool)?
    var onGeometryValue: ((Any) -> Void)?

    private weak var observedScrollView: UIScrollView?
    private var observations: [NSKeyValueObservation] = []
    private var lastPhase: CompatScrollPhase?
    private var lastVisibility: Bool?
    private var lastGeometryValue: Any?

    override func didMoveToWindow() {
        super.didMoveToWindow()
        attachIfNeeded()
        emitCurrentState()
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        attachIfNeeded()
        emitCurrentState()
    }

    override func removeFromSuperview() {
        // 视图离开层级时释放 KVO，避免向已复用的行继续回调。
        observations.forEach { $0.invalidate() }
        observations.removeAll()
        observedScrollView = nil
    }

    private func attachIfNeeded() {
        guard window != nil else { return }
        // 已经挂上一个仍在窗口里的滚动视图就不再重新搜索：滚动视图的身份对同一
        // 观测点实例是稳定的，List/ScrollView 重建时 SwiftUI 会给出新的观测点实例。
        if let observed = observedScrollView, observed.window != nil { return }
        guard let scrollView = enclosingScrollView() ?? siblingScrollView() else { return }
        if scrollView !== observedScrollView {
            attach(scrollView)
        }
    }

    /// 观测点在滚动内容内部（行内 background）：沿父链向上找承载它的 UIScrollView。
    private func enclosingScrollView() -> UIScrollView? {
        var ancestor: UIView? = superview
        while let view = ancestor {
            if let scrollView = view as? UIScrollView {
                return scrollView
            }
            ancestor = view.superview
        }
        return nil
    }

    /// 观测点直接挂在 ScrollView/List 自身上：SwiftUI 把 `background` 做成滚动视图的兄弟
    /// 节点，父链里没有 UIScrollView，只能从最近的祖先开始按层级由浅到深找第一个。
    /// 由浅到深保证拿到的是外层滚动视图，不会误抓页内更深的嵌套列表。
    private func siblingScrollView() -> UIScrollView? {
        var ancestor: UIView? = superview
        while let view = ancestor {
            if let scrollView = Self.firstScrollView(in: view, skipping: self) {
                return scrollView
            }
            ancestor = view.superview
        }
        return nil
    }

    private static func firstScrollView(in root: UIView, skipping observer: UIView) -> UIScrollView? {
        var queue = root.subviews.filter { $0 !== observer && !$0.isHidden }
        while !queue.isEmpty {
            let view = queue.removeFirst()
            // -swift-version 5 下 `is` 收窄不会自动把 UIView 转成 UIScrollView，显式 as?。
            if let scrollView = view as? UIScrollView {
                return scrollView
            }
            queue.append(contentsOf: view.subviews.filter { !$0.isHidden })
        }
        return nil
    }

    private func attach(_ scrollView: UIScrollView) {
        observations.forEach { $0.invalidate() }
        observedScrollView = scrollView
        let options: NSKeyValueObservingOptions = [.initial, .new]
        observations = [
            scrollView.observe(\.contentOffset, options: options) { [weak self] _, _ in
                self?.emitGeometry()
                self?.emitVisibility()
            },
            scrollView.observe(\.contentSize, options: options) { [weak self] _, _ in
                self?.emitGeometry()
                self?.emitVisibility()
            },
            scrollView.observe(\.adjustedContentInset, options: options) { [weak self] _, _ in
                self?.emitGeometry()
                self?.emitVisibility()
            },
            scrollView.observe(\.isDragging, options: options) { [weak self] _, _ in
                self?.emitPhase()
            },
            scrollView.observe(\.isDecelerating, options: options) { [weak self] _, _ in
                self?.emitPhase()
            },
        ]
        emitGeometry()
        emitPhase()
        emitVisibility()
    }

    private func emitCurrentState() {
        guard observedScrollView != nil else { return }
        emitGeometry()
        emitPhase()
        emitVisibility()
    }

    private func emitGeometry() {
        guard let scrollView = observedScrollView else { return }
        if onGeometry != nil {
            onGeometry?(CompatScrollGeometry(scrollView))
        }
        guard let sample = geometryValueSample, onGeometryValue != nil else { return }
        let value = sample(CompatScrollGeometry(scrollView))
        if let last = lastGeometryValue, geometryValueIsEqual?(last, value) == true { return }
        lastGeometryValue = value
        onGeometryValue?(value)
    }

    private func emitPhase() {
        guard let scrollView = observedScrollView, onPhase != nil else { return }
        let phase = CompatScrollPhase.current(scrollView)
        guard phase != lastPhase else { return }
        lastPhase = phase
        onPhase?(phase)
    }

    private func emitVisibility() {
        guard let scrollView = observedScrollView, onVisibility != nil, window != nil else { return }
        guard bounds.width > 0, bounds.height > 0 else {
            reportVisibility(false)
            return
        }
        let frameInScroll = convert(bounds, to: scrollView)
        let visibleArea = CGRect(origin: scrollView.contentOffset, size: scrollView.bounds.size)
        let intersection = frameInScroll.intersection(visibleArea)
        let ratio = intersection.isNull || intersection.isEmpty
            ? 0
            : (intersection.width * intersection.height) / (bounds.width * bounds.height)
        reportVisibility(ratio >= visibilityThreshold)
    }

    private func reportVisibility(_ visible: Bool) {
        guard visible != lastVisibility else { return }
        lastVisibility = visible
        onVisibility?(visible)
    }
}

private struct CompatScrollObservationRepresentable: UIViewRepresentable {
    var tracksGeometry = false
    var tracksPhase = false
    var visibilityThreshold: CGFloat?

    let onGeometry: ((CompatScrollGeometry) -> Void)?
    let onPhase: ((CompatScrollPhase) -> Void)?
    let onVisibility: ((Bool) -> Void)?
    var geometryValueSample: ((CompatScrollGeometry) -> Any)?
    var geometryValueIsEqual: ((Any, Any) -> Bool)?
    var onGeometryValue: ((Any) -> Void)?

    func makeUIView(context: Context) -> CompatScrollObservationView {
        let view = CompatScrollObservationView()
        view.isUserInteractionEnabled = false
        apply(to: view)
        return view
    }

    func updateUIView(_ uiView: CompatScrollObservationView, context: Context) {
        apply(to: uiView)
    }

    private func apply(to view: CompatScrollObservationView) {
        view.onGeometry = tracksGeometry ? onGeometry : nil
        view.onPhase = tracksPhase ? onPhase : nil
        view.onVisibility = visibilityThreshold != nil ? onVisibility : nil
        view.visibilityThreshold = visibilityThreshold ?? 0.01
        view.geometryValueSample = geometryValueSample
        view.geometryValueIsEqual = geometryValueIsEqual
        view.onGeometryValue = onGeometryValue
    }
}

// MARK: - 调用点入口

extension View {
    /// 采样值形态：iOS 18+ 用原生 `onScrollGeometryChange(for:)`（只在值变化时回调）；
    /// 更低版本用 KVO 观测加等值去重，保持同样的回调频率。
    @ViewBuilder
    func compatOnScrollGeometryChange<Value: Equatable>(
        for: Value.Type = Value.self,
        transform: @escaping (CompatScrollGeometry) -> Value,
        action: @escaping (Value) -> Void
    ) -> some View {
        if #available(iOS 18.0, *) {
            self.onScrollGeometryChange(for: Value.self) { geometry in
                transform(CompatScrollGeometry(geometry))
            } action: { _, newValue in
                action(newValue)
            }
        } else {
            background(
                CompatScrollObservationRepresentable(
                    tracksGeometry: true,
                    onGeometry: nil,
                    onPhase: nil,
                    onVisibility: nil,
                    geometryValueSample: { transform($0) },
                    geometryValueIsEqual: { ($0 as? Value) == ($1 as? Value) },
                    onGeometryValue: { value in
                        if let value = value as? Value {
                            action(value)
                        }
                    }
                )
            )
        }
    }

    /// iOS 18+ 用原生 `onScrollPhaseChange`；更低版本由 isDragging / isDecelerating 映射。
    @ViewBuilder
    func compatOnScrollPhaseChange(action: @escaping (CompatScrollPhase) -> Void) -> some View {
        if #available(iOS 18.0, *) {
            self.onScrollPhaseChange { _, newPhase in
                action(CompatScrollPhase(newPhase))
            }
        } else {
            background(
                CompatScrollObservationRepresentable(
                    tracksPhase: true,
                    onGeometry: nil,
                    onPhase: action,
                    onVisibility: nil
                )
            )
        }
    }

    /// iOS 18+ 用原生 `onScrollVisibilityChange`；更低版本按宿主滚动可视区相交比例判定。
    @ViewBuilder
    func compatOnScrollVisibilityChange(
        threshold: CGFloat = 0.01,
        action: @escaping (Bool) -> Void
    ) -> some View {
        if #available(iOS 18.0, *) {
            // Apple Docs 实测签名是 `onScrollVisibilityChange(threshold: Double = 0.5, _ action:)`，
            // 第二个参数没有外部标签，写成 `action:` 会报 extraneous argument label。
            self.onScrollVisibilityChange(threshold: threshold) { visible in
                action(visible)
            }
        } else {
            background(
                CompatScrollObservationRepresentable(
                    visibilityThreshold: threshold,
                    onGeometry: nil,
                    onPhase: nil,
                    onVisibility: action
                )
            )
        }
    }
}

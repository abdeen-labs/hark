//
//  AxisComponents.swift
//  Hark
//
//  The app's own pieces on top of the shared primitives: bordered modules
//  with a label bar, notices, fields, metrics, ledger indexes, the page
//  eyebrow, the environmental index, the dot-matrix texture, the column
//  ruler, and the route schematic.
//

import SwiftUI
import UIKit

// MARK: - Modules

/// A bordered region with a label bar — not a card. One-pixel rule, a
/// surface a step above the paper, no radius, no shadow.
struct Module<Content: View, Trailing: View>: View {
    enum Variant {
        case plain
        /// No surface of its own; the rule alone frames it.
        case flat
        /// A signal strip down the left edge.
        case signal
        /// A hatched hazard band down the left edge of a destructive region.
        case hazard
        /// Corner brackets at two opposite corners.
        case marked
    }

    var index: String?
    let label: String
    var variant: Variant = .plain
    var flush = false
    @ViewBuilder var content: () -> Content
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                if let index {
                    IndexLabel(index)
                }
                Meta(label, color: Axis.inkSubtle)
                Spacer(minLength: 8)
                trailing()
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
            .frame(minHeight: 38)
            Hairline()
            content()
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(flush ? EdgeInsets() : EdgeInsets(top: 20, leading: 16, bottom: 20, trailing: 16))
        }
        .padding(.leading, edgeWidth)
        .background(variant == .flat ? Color.clear : Axis.surface)
        .overlay(alignment: .leading) { edge }
        .overlay(Rectangle().strokeBorder(border, lineWidth: 1))
        .overlay {
            if variant == .marked {
                CornerMarks()
            }
        }
    }

    private var edgeWidth: CGFloat {
        switch variant {
        case .signal: 4
        case .hazard: 8
        default: 0
        }
    }

    @ViewBuilder private var edge: some View {
        switch variant {
        case .signal:
            Rectangle().fill(Axis.signal).frame(width: 4)
        case .hazard:
            HazardBand().frame(width: 8)
        default:
            EmptyView()
        }
    }

    private var border: Color {
        switch variant {
        case .signal: Axis.signalLine
        case .hazard: Axis.lineStrong
        default: Axis.line
        }
    }
}

extension Module where Trailing == EmptyView {
    init(
        index: String? = nil,
        label: String,
        variant: Variant = .plain,
        flush: Bool = false,
        @ViewBuilder content: @escaping () -> Content
    ) {
        self.init(index: index, label: label, variant: variant, flush: flush, content: content) {
            EmptyView()
        }
    }
}

/// Hazard marking: signal stripes at 135°, for the edge of a region whose
/// control is destructive.
struct HazardBand: View {
    var body: some View {
        Canvas { context, size in
            let period: CGFloat = 11
            let band: CGFloat = 5
            var path = Path()
            var x = -size.height
            while x < size.width + size.height {
                path.move(to: CGPoint(x: x, y: 0))
                path.addLine(to: CGPoint(x: x + band, y: 0))
                path.addLine(to: CGPoint(x: x + band - size.height, y: size.height))
                path.addLine(to: CGPoint(x: x - size.height, y: size.height))
                path.closeSubpath()
                x += period
            }
            context.fill(path, with: .color(Axis.signal.opacity(0.8)))
        }
        .clipped()
        .accessibilityHidden(true)
    }
}

/// Corner brackets at the top-left and bottom-right corners of a module.
struct CornerMarks: View {
    var size: CGFloat = 9
    var color: Color = Axis.inkSubtle

    var body: some View {
        GeometryReader { proxy in
            Path { path in
                path.move(to: CGPoint(x: 0.5, y: size))
                path.addLine(to: CGPoint(x: 0.5, y: 0.5))
                path.addLine(to: CGPoint(x: size, y: 0.5))
                let w = proxy.size.width
                let h = proxy.size.height
                path.move(to: CGPoint(x: w - size, y: h - 0.5))
                path.addLine(to: CGPoint(x: w - 0.5, y: h - 0.5))
                path.addLine(to: CGPoint(x: w - 0.5, y: h - size))
            }
            .stroke(color, lineWidth: 1)
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

// MARK: - Notices

/// A notice: a mono kind label, the message, and a rule down the left edge
/// in the kind's colour.
struct Notice: View {
    enum Kind {
        case error, ok, warn, plain
    }

    var kind: Kind = .plain
    let message: String

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 14) {
            Meta(label, color: tone)
            Text(message)
                .font(AxisType.copy(14))
                .foregroundStyle(Axis.ink)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(EdgeInsets(top: 12, leading: 16, bottom: 12, trailing: 16))
        .background(kind == .error ? Axis.signalWash : Axis.surface)
        .overlay(Rectangle().strokeBorder(Axis.line, lineWidth: 1))
        .overlay(alignment: .leading) {
            Rectangle().fill(tone).frame(width: 3)
        }
    }

    private var label: String {
        switch kind {
        case .error: "Error"
        case .ok: "OK"
        case .warn: "Warning"
        case .plain: "Notice"
        }
    }

    private var tone: Color {
        switch kind {
        case .error: Axis.danger
        case .ok: Axis.ok
        case .warn: Axis.warn
        case .plain: Axis.inkSubtle
        }
    }
}

// MARK: - Fields

/// A labelled field: mono label above, a one-pixel frame around the control,
/// and the signal colour on the frame while it has focus.
struct FieldFrame<Content: View>: View {
    var label: String?
    var hint: String?
    var focused = false
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let label {
                Meta(label, color: Axis.inkSubtle)
            }
            content()
                .font(AxisType.copy(15))
                .foregroundStyle(Axis.ink)
                .tint(Axis.signalText)
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                .background(Axis.field)
                .overlay(
                    RoundedRectangle(cornerRadius: Axis.Radius.sm, style: .continuous)
                        .strokeBorder(focused ? Axis.signal : Axis.lineStrong, lineWidth: focused ? 1.5 : 1)
                )
                .animation(Axis.Motion.quick, value: focused)
            if let hint {
                Text(hint)
                    .font(AxisType.copy(12))
                    .foregroundStyle(Axis.inkFaint)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

// MARK: - Metrics

/// A number at architectural scale with its label beneath. The optional
/// denominator sits small and faint after the value.
struct Metric: View {
    let value: String
    var of: String?
    let label: String
    var size: CGFloat = 48
    var color: Color = Axis.ink
    var alignment: HorizontalAlignment = .leading

    var body: some View {
        VStack(alignment: alignment, spacing: 10) {
            HStack(alignment: .lastTextBaseline, spacing: 0) {
                Text(value)
                    .axisMetric(size)
                    .foregroundStyle(color)
                    .contentTransition(.numericText())
                if let of {
                    Text("/" + of)
                        .font(.system(size: size * 0.42, weight: .medium))
                        .monospacedDigit()
                        .tracking(AxisType.tracking(-0.02, at: size * 0.42))
                        .foregroundStyle(Axis.inkDisabled)
                }
            }
            .lineLimit(1)
            .minimumScaleFactor(0.5)
            .padding(.top, -size * 0.18)
            .padding(.bottom, -size * 0.16)
            Meta(label)
        }
    }
}

/// A page title in the display register, its line box cropped to the cap
/// height and its left edge pulled into the gutter by the optical margin.
struct DisplayTitle: View {
    let text: String
    var size: CGFloat = 60

    var body: some View {
        Text(text)
            .axisDisplay(size)
            .foregroundStyle(Axis.ink)
            .lineLimit(1)
            .minimumScaleFactor(0.6)
            .padding(.top, -size * 0.2)
            .padding(.bottom, -size * 0.08)
            .offset(x: -size * 0.04)
            .accessibilityAddTraits(.isHeader)
    }
}

// MARK: - Spec rows

/// A definition row: the mono label in a fixed column, the value beside it.
/// Long-press copies the value. An identifier is recognized by its ends and
/// rarely needs reading whole, so it shows middle-truncated on one line and
/// expands on tap.
struct SpecRow: View {
    let label: String
    let value: String
    var mono = true
    var expandable = false

    @State private var expanded = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 16) {
            Meta(label)
                .frame(width: 100, alignment: .leading)
            Text(value)
                .font(mono ? AxisType.mono(12) : AxisType.copy(14))
                .foregroundStyle(Axis.inkMuted)
                .lineLimit(expandable && !expanded ? 1 : nil)
                .truncationMode(expandable ? .middle : .tail)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 9)
        .contentShape(Rectangle())
        .onTapGesture {
            guard expandable else { return }
            withAnimation(Axis.Motion.quick) { expanded.toggle() }
        }
        .contextMenu {
            Button("Copy", systemImage: "doc.on.doc") {
                UIPasteboard.general.string = value
            }
        }
    }
}

// MARK: - Flow layout

/// Lays its children out in rows, wrapping at the proposed width. Tags use
/// it so a row of them never truncates.
nonisolated struct FlowRow: Layout {
    var spacing: CGFloat = 6
    var lineSpacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0
        var widest: CGFloat = 0
        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x > 0, x + size.width > width {
                x = 0
                y += rowHeight + lineSpacing
                rowHeight = 0
            }
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: width == .infinity ? widest : width, height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX
        var y = bounds.minY
        var rowHeight: CGFloat = 0
        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x > bounds.minX, x + size.width > bounds.maxX {
                x = bounds.minX
                y += rowHeight + lineSpacing
                rowHeight = 0
            }
            subview.place(at: CGPoint(x: x, y: y), proposal: .unspecified)
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
    }
}

nonisolated enum AppInfo {
    /// `3.0 (1)` — the marketing version and the build.
    static var version: String {
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "1.0"
        let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "1"
        return "\(version) (\(build))"
    }
}

// MARK: - Ledger

/// The running index down the left of a ledger. Decorative: two digits in
/// the disabled ink, hidden from assistive technology.
struct LedgerIndex: View {
    let number: Int
    var width: CGFloat = 28

    var body: some View {
        Text(String(format: "%02d", number))
            .font(AxisType.meta(11))
            .monospacedDigit()
            .foregroundStyle(Axis.inkDisabled)
            .frame(width: width, alignment: .leading)
            .accessibilityHidden(true)
    }
}

/// Clock helpers for the ledger's mono timestamps.
nonisolated enum AxisClock {
    /// A compact age: `now`, `4m`, `3h`, `6d`, then the day.
    static func age(_ date: Date, now: Date = .now) -> String {
        let seconds = now.timeIntervalSince(date)
        switch seconds {
        case ..<45:
            return "now"
        case ..<3600:
            return "\(Int(seconds / 60))m"
        case ..<86400:
            return "\(Int(seconds / 3600))h"
        case ..<(7 * 86400):
            return "\(Int(seconds / 86400))d"
        default:
            return date.formatted(.dateTime.day(.twoDigits).month(.abbreviated))
        }
    }

    /// A 24-hour wall-clock stamp, `21:24:03`, in the local zone.
    static func stamp(_ date: Date) -> String {
        let parts = Calendar.current.dateComponents([.hour, .minute, .second], from: date)
        return String(format: "%02d:%02d:%02d", parts.hour ?? 0, parts.minute ?? 0, parts.second ?? 0)
    }

    /// A wall-clock stamp without seconds, `21:24`.
    static func short(_ date: Date) -> String {
        let parts = Calendar.current.dateComponents([.hour, .minute], from: date)
        return String(format: "%02d:%02d", parts.hour ?? 0, parts.minute ?? 0)
    }
}

// MARK: - Countdown

/// A live countdown to a question's deadline, in mono.
struct ExpiryCountdown: View {
    let expiresAt: Date
    var size: CGFloat = 12
    var color: Color = Axis.inkSubtle

    var body: some View {
        if expiresAt > .now {
            Text(timerInterval: Date.now ... expiresAt, countsDown: true)
                .font(AxisType.mono(size, weight: .medium))
                .monospacedDigit()
                .foregroundStyle(color)
        } else {
            Meta("Expired", size: max(size - 2, 9), color: Axis.inkFaint)
        }
    }
}

// MARK: - Kind helpers

nonisolated enum InteractionKindDisplay {
    static func label(_ kind: String) -> String {
        switch kind {
        case "approval": "Approval"
        case "yes_no": "Yes / No"
        case "reply": "Reply"
        default: kind.replacingOccurrences(of: "_", with: " ")
        }
    }
}

// MARK: - Empty and loading

/// The note a region shows when it holds nothing. Plain copy, left-aligned;
/// the composition around it does the work.
struct EmptyNote: View {
    let text: String
    var detail: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(text)
                .font(AxisType.copy(15, weight: .medium))
                .foregroundStyle(Axis.inkSubtle)
            if let detail {
                Text(detail)
                    .font(AxisType.copy(14))
                    .foregroundStyle(Axis.inkFaint)
            }
        }
        .fixedSize(horizontal: false, vertical: true)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.vertical, 22)
    }
}

/// A blinking light and a word, in place of a spinner.
struct LoadingMark: View {
    var text = "Loading"

    var body: some View {
        HStack(spacing: 8) {
            StatusLight(color: Axis.signal, size: 6, blinking: true)
            Meta(text, color: Axis.inkSubtle)
        }
        .accessibilityElement(children: .combine)
    }
}

// MARK: - Page furniture

/// The mono line above a title: the section index, its label, and whatever
/// metadata sits at the far end.
struct Eyebrow<Trailing: View>: View {
    let index: String
    let label: String
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        HStack(spacing: 8) {
            IndexLabel(index)
            Meta(label)
            Spacer(minLength: 12)
            trailing()
        }
    }
}

extension Eyebrow where Trailing == EmptyView {
    init(index: String, label: String) {
        self.init(index: index, label: label) { EmptyView() }
    }
}

/// The section number at the scale of the page, drawn faint behind a head
/// and cropped by its edges.
struct EnvironmentalIndex: View {
    let text: String
    var size: CGFloat = 220

    var body: some View {
        Text(text)
            .font(.system(size: size, weight: .semibold))
            .monospacedDigit()
            .tracking(AxisType.tracking(-0.06, at: size))
            .foregroundStyle(Axis.ink.opacity(0.045))
            .lineLimit(1)
            .fixedSize()
            .allowsHitTesting(false)
            .accessibilityHidden(true)
    }
}

/// The one texture: a dot matrix at the grid's pitch, fading out below the
/// fold.
struct DotMatrix: View {
    var body: some View {
        Canvas { context, size in
            let pitch: CGFloat = 24
            let radius: CGFloat = 1.1
            var dots = Path()
            var y: CGFloat = 12
            while y < size.height {
                var x: CGFloat = 12
                while x < size.width {
                    dots.addEllipse(in: CGRect(x: x - radius, y: y - radius, width: radius * 2, height: radius * 2))
                    x += pitch
                }
                y += pitch
            }
            context.fill(dots, with: .color(Axis.ink.opacity(0.085)))
        }
        .mask(
            LinearGradient(
                stops: [
                    .init(color: .black, location: 0),
                    .init(color: .black, location: 0.45),
                    .init(color: .clear, location: 1),
                ],
                startPoint: .top,
                endPoint: .bottom
            )
        )
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

/// Column ticks: one mark at the left edge of each of the four columns and
/// one at the right edge of the grid.
struct ColumnRuler: View {
    var height: CGFloat = 6

    var body: some View {
        Canvas { context, size in
            let columns = CGFloat(Axis.columns)
            let column = (size.width - (columns - 1) * Axis.gap) / columns
            var ticks = Path()
            for i in 0 ... Axis.columns {
                let x = i == Axis.columns ? size.width - 1 : CGFloat(i) * (column + Axis.gap)
                ticks.addRect(CGRect(x: x, y: 0, width: 1, height: size.height))
            }
            context.fill(ticks, with: .color(Axis.lineStrong))
        }
        .frame(height: height)
        .padding(.horizontal, Axis.gutter)
        .accessibilityHidden(true)
    }
}

// MARK: - Schematic

/// Linework for the route a push takes: nodes on a rule, one segment in
/// signal, mono labels beneath. Purely decorative.
struct Schematic: View {
    var nodes: [String] = ["Agent", "harkd", "APNs", "Phone"]
    /// The segment drawn in signal, counted from the left.
    var signalSegment = 2
    /// A label under the signal segment.
    var note: String?
    var height: CGFloat = 56

    var body: some View {
        Canvas { context, size in
            let count = nodes.count
            guard count > 1 else { return }
            let inset: CGFloat = 28
            let y: CGFloat = 16
            let span = (size.width - inset * 2) / CGFloat(count - 1)
            let xs = (0 ..< count).map { inset + CGFloat($0) * span }

            for i in 0 ..< count - 1 {
                let isSignal = i == signalSegment
                var line = Path()
                line.move(to: CGPoint(x: xs[i] + 4, y: y))
                line.addLine(to: CGPoint(x: xs[i + 1] - 4, y: y))
                context.stroke(
                    line,
                    with: .color(isSignal ? Axis.signal : Axis.inkFaint.opacity(0.7)),
                    lineWidth: isSignal ? 1.5 : 1
                )
                if isSignal {
                    var head = Path()
                    let tip = CGPoint(x: xs[i + 1] - 5, y: y)
                    head.move(to: tip)
                    head.addLine(to: CGPoint(x: tip.x - 5, y: y - 3))
                    head.addLine(to: CGPoint(x: tip.x - 5, y: y + 3))
                    head.closeSubpath()
                    context.fill(head, with: .color(Axis.signal))
                }
            }

            for (i, x) in xs.enumerated() {
                let node = CGRect(x: x - 4, y: y - 4, width: 8, height: 8)
                context.fill(Path(node), with: .color(Axis.paper))
                let lit = i == signalSegment + 1
                context.stroke(Path(node), with: .color(lit ? Axis.signal : Axis.inkSubtle), lineWidth: 1)
                let label = Text(nodes[i].uppercased())
                    .font(AxisType.meta(9))
                    .tracking(0.9)
                    .foregroundStyle(lit ? Axis.ink : Axis.inkFaint)
                context.draw(label, at: CGPoint(x: x, y: y + 14), anchor: .top)
            }

            if let note, signalSegment < count - 1 {
                let mid = (xs[signalSegment] + xs[signalSegment + 1]) / 2
                let text = Text(note.uppercased())
                    .font(AxisType.meta(9))
                    .tracking(0.9)
                    .foregroundStyle(Axis.signalText)
                context.draw(text, at: CGPoint(x: mid, y: y - 10), anchor: .bottom)
            }
        }
        .frame(height: height)
        .accessibilityHidden(true)
    }
}

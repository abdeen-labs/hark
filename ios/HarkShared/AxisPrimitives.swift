//
//  AxisPrimitives.swift
//  Hark
//
//  The pieces the app and the Live Activity both draw with: mono metadata,
//  framed tags with a square status light, hairline rules, the thin progress
//  rule, and the instrument-control button. Everything here is flat — a
//  one-pixel frame and a surface, never a shadow.
//

import SwiftUI

// MARK: - Metadata

/// A line of mono metadata: uppercase, tracked, one line.
struct Meta: View {
    let text: String
    var size: CGFloat = 10
    var color: Color = Axis.inkFaint

    init(_ text: String, size: CGFloat = 10, color: Color = Axis.inkFaint) {
        self.text = text
        self.size = size
        self.color = color
    }

    var body: some View {
        Text(text)
            .axisMeta(size)
            .textCase(.uppercase)
            .foregroundStyle(color)
            .lineLimit(1)
    }
}

/// A mono index in the signal colour: the `01` that leads a label.
struct IndexLabel: View {
    let text: String
    var size: CGFloat = 10
    var color: Color = Axis.signalText

    init(_ text: String, size: CGFloat = 10, color: Color = Axis.signalText) {
        self.text = text
        self.size = size
        self.color = color
    }

    var body: some View {
        Text(text)
            .font(AxisType.meta(size))
            .monospacedDigit()
            .tracking(AxisType.tracking(0.08, at: size))
            .foregroundStyle(color)
            .accessibilityHidden(true)
    }
}

// MARK: - Lights and rules

/// A square status light. A warning's light is the square rotated. Blinks in
/// hard steps when asked to, unless motion is reduced.
struct StatusLight: View {
    var color: Color = Axis.signal
    var size: CGFloat = 6
    var blinking = false
    var rotated = false

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        if blinking, !reduceMotion {
            TimelineView(.periodic(from: .now, by: 1)) { context in
                let on = Int(context.date.timeIntervalSinceReferenceDate.rounded(.down)) % 2 == 0
                square.opacity(on ? 1 : 0.2)
            }
        } else {
            square
        }
    }

    private var square: some View {
        Rectangle()
            .fill(color)
            .frame(width: size, height: size)
            .rotationEffect(rotated ? .degrees(45) : .zero)
    }
}

/// A one-pixel rule.
struct Hairline: View {
    var color: Color = Axis.line
    var vertical = false

    var body: some View {
        Rectangle()
            .fill(color)
            .frame(
                maxWidth: vertical ? 1 : .infinity,
                maxHeight: vertical ? .infinity : 1
            )
    }
}

/// A progress rule: two points tall, square-ended, with the tint drawn over a
/// faint track. Nothing is drawn when there is no progress to report.
struct ThinBar: View {
    let progress: Double?
    var tint: Color = Axis.signal
    var height: CGFloat = 2

    var body: some View {
        if let progress {
            GeometryReader { proxy in
                ZStack(alignment: .leading) {
                    Rectangle().fill(Axis.lineStrong)
                    Rectangle()
                        .fill(tint)
                        .frame(width: proxy.size.width * min(max(progress, 0), 1))
                }
            }
            .frame(height: height)
        }
    }
}

// MARK: - Tags

/// A status or kind in a one-pixel frame. State tags carry a square light in
/// their colour; kind tags do not. A warning is the highlighter chip with a
/// rotated square; a fault is the filled alarm field. Both take carbon ink.
struct Tag: View {
    enum Tone {
        case kind, ok, warn, danger, muted, signal
    }

    let text: String
    var tone: Tone = .kind
    var light = false

    init(_ text: String, tone: Tone = .kind, light: Bool = false) {
        self.text = text
        self.tone = tone
        self.light = light
    }

    var body: some View {
        HStack(spacing: 6) {
            if light {
                Rectangle()
                    .fill(color)
                    .frame(width: 5, height: 5)
                    .rotationEffect(tone == .warn ? .degrees(45) : .zero)
            }
            Text(text)
                .axisMeta(10)
                .textCase(.uppercase)
                .lineLimit(1)
        }
        .foregroundStyle(color)
        .padding(.horizontal, 7)
        .padding(.vertical, 4)
        .background(fill, in: RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous)
                .strokeBorder(border, lineWidth: 1)
        )
    }

    private var color: Color {
        switch tone {
        case .kind: Axis.inkSubtle
        case .ok: Axis.ok
        case .warn, .danger: Axis.onField
        case .muted: Axis.inkFaint
        case .signal: Axis.signalText
        }
    }

    private var fill: Color {
        switch tone {
        case .warn: Axis.warnChip
        case .danger: Axis.alarmField
        default: .clear
        }
    }

    private var border: Color {
        switch tone {
        case .kind: Axis.lineStrong
        case .ok: Axis.okLine
        case .warn, .danger: .clear
        case .muted: Axis.line
        case .signal: Axis.signalLine
        }
    }
}

/// Maps the wire vocabulary — interaction results, delivery statuses, device
/// states — onto a tone. Unknown words render as a kind tag.
nonisolated enum AxisState {
    static func tone(_ state: String) -> Tag.Tone {
        switch state {
        case "approved", "yes", "replied", "accepted", "active", "consumed", "ok", "delivered", "live", "ready":
            .ok
        case "denied", "no", "failed", "error", "retired", "critical":
            .danger
        case "pending", "partial", "starting", "warn", "time_sensitive":
            .warn
        case "expired", "canceled", "ended", "no_devices", "inactive":
            .muted
        default:
            .kind
        }
    }

    static func label(_ state: String) -> String {
        state.replacingOccurrences(of: "_", with: " ")
    }
}

/// A state tag straight from a wire value.
struct StateTag: View {
    let state: String

    var body: some View {
        Tag(AxisState.label(state), tone: AxisState.tone(state), light: true)
    }
}

// MARK: - Buttons

/// An instrument control: compact, square-cornered, labelled in capitals, and
/// a press that compresses rather than lifts.
struct InstrumentButtonStyle: ButtonStyle {
    enum Kind {
        case primary, secondary, danger, ghost
    }

    enum Arrow {
        case forward, back
    }

    var kind: Kind = .secondary
    var arrow: Arrow?
    var compact = false
    var fill = true
    /// Overrides the signal colour for a primary control — a Live Activity
    /// answers in the accent its server chose.
    var tint: Color?
    /// The label ink on a tinted primary control.
    var ink: Color?

    @Environment(\.isEnabled) private var isEnabled
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func makeBody(configuration: Configuration) -> some View {
        let pressed = configuration.isPressed
        let size: CGFloat = compact ? 11 : 12
        HStack(spacing: 10) {
            if arrow == .back {
                Text("←")
                    .axisControl(size)
                    .offset(x: pressed && !reduceMotion ? -4 : 0)
            }
            configuration.label
                .font(AxisType.control(size))
                .tracking(AxisType.tracking(AxisType.controlTracking, at: size))
            if arrow == .forward {
                Text("→")
                    .axisControl(size)
                    .offset(x: pressed && !reduceMotion ? 4 : 0)
            }
        }
        .textCase(.uppercase)
        .lineLimit(1)
        .minimumScaleFactor(0.85)
        .foregroundStyle(foreground(pressed: pressed))
        .padding(.horizontal, compact ? 10 : 14)
        .frame(maxWidth: fill ? .infinity : nil, minHeight: compact ? 36 : 44)
        .background(background(pressed: pressed))
        .overlay(
            RoundedRectangle(cornerRadius: Axis.Radius.sm, style: .continuous)
                .strokeBorder(border(pressed: pressed), lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: Axis.Radius.sm, style: .continuous))
        .contentShape(Rectangle())
        .scaleEffect(pressed && !reduceMotion ? Axis.Motion.press : 1)
        .animation(Axis.Motion.quick, value: pressed)
    }

    private var field: Color { tint ?? Axis.signalField }
    private var fieldInk: Color { ink ?? Axis.onSignal }

    private func foreground(pressed: Bool) -> Color {
        guard isEnabled else {
            return kind == .primary ? fieldInk.opacity(0.55) : Axis.inkDisabled
        }
        switch kind {
        case .primary: return fieldInk
        case .secondary: return Axis.ink
        case .danger: return pressed ? Axis.onSignal : Axis.signalText
        case .ghost: return pressed ? Axis.ink : Axis.inkSubtle
        }
    }

    private func background(pressed: Bool) -> Color {
        guard isEnabled else {
            return kind == .primary ? field.opacity(0.35) : .clear
        }
        switch kind {
        case .primary: return field
        case .secondary: return pressed ? Axis.surface3 : .clear
        case .danger: return pressed ? Axis.signalField : .clear
        case .ghost: return .clear
        }
    }

    private func border(pressed: Bool) -> Color {
        guard isEnabled else {
            return kind == .primary ? .clear : Axis.line
        }
        switch kind {
        case .primary: return .clear
        case .secondary: return pressed ? Axis.ink : Axis.lineStrong
        case .danger: return pressed ? Axis.signalField : Axis.signalLine
        case .ghost: return pressed ? Axis.lineStrong : .clear
        }
    }
}

extension ButtonStyle where Self == InstrumentButtonStyle {
    static func instrument(
        _ kind: InstrumentButtonStyle.Kind = .secondary,
        arrow: InstrumentButtonStyle.Arrow? = nil,
        compact: Bool = false,
        fill: Bool = true,
        tint: Color? = nil,
        ink: Color? = nil
    ) -> InstrumentButtonStyle {
        InstrumentButtonStyle(kind: kind, arrow: arrow, compact: compact, fill: fill, tint: tint, ink: ink)
    }
}

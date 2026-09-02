//
//  ActivityStyleSupport.swift
//  HarkWidgets
//
//  Vocabulary resolution for the content state: symbols, styles, and the
//  pieces every layout shares. Unknown values use the standard fallback.
//

import AppIntents
import SwiftUI
import WidgetKit

nonisolated enum ActivitySymbol {
    /// Maps the server's symbol vocabulary onto SF Symbols.
    static func systemName(_ raw: String) -> String {
        switch raw {
        case "code": "chevron.left.forwardslash.chevron.right"
        case "build": "hammer.fill"
        case "success": "checkmark.circle.fill"
        case "warning": "exclamationmark.triangle.fill"
        default: "apple.terminal"
        }
    }
}

nonisolated enum ActivityStyleKind: String {
    case standard, ring, hero, terminal, steps
    case approval, shell, verdict, signal

    var isInteractive: Bool {
        switch self {
        case .approval, .shell, .verdict, .signal: true
        default: false
        }
    }

    /// Resolves a content state's style. Unknown strings render as standard,
    /// and an interactive style without a question behind it does too — a
    /// pair of buttons that answer nothing must never be drawn.
    static func resolve(_ state: HarkActivityAttributes.ContentState) -> ActivityStyleKind {
        guard let style = ActivityStyleKind(rawValue: state.style) else { return .standard }
        if style.isInteractive, state.interaction == nil { return .standard }
        return style
    }
}

extension HarkActivityAttributes.ContentState {
    var accent: Color { .harkAccent(accentColor) }
    var accentInk: Color { .harkAccentInk(accentColor) }
    var symbolName: String { ActivitySymbol.systemName(symbol) }
    var isPrivate: Bool { privacyMode == "private" }
}

extension View {
    /// Marks content the owner asked to keep off a locked screen. iOS
    /// redacts privacy-sensitive views while the device is locked and shows
    /// them once it is not.
    @ViewBuilder
    func harkMasked(_ masked: Bool) -> some View {
        if masked {
            privacySensitive()
        } else {
            self
        }
    }
}

/// The square glyph plate the progress layouts lead with: a one-pixel frame
/// in the accent with the symbol inside it.
struct SymbolPlate: View {
    let state: HarkActivityAttributes.ContentState
    var size: CGFloat = 34

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous)
                .fill(Axis.surface2)
            RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous)
                .strokeBorder(state.accent.opacity(0.55), lineWidth: 1)
            Image(systemName: state.symbolName)
                .font(.system(size: size * 0.42, weight: .semibold))
                .foregroundStyle(state.accent)
        }
        .frame(width: size, height: size)
    }
}

/// A percent readout in mono, or nothing when there is no progress.
struct PercentText: View {
    let progress: Double?
    var size: CGFloat = 13
    var color: Color = Axis.ink

    var body: some View {
        if let progress {
            Text(clamped(progress), format: .percent.precision(.fractionLength(0)))
                .font(AxisType.mono(size, weight: .semibold))
                .monospacedDigit()
                .foregroundStyle(color)
        }
    }

    private func clamped(_ value: Double) -> Double {
        min(max(value, 0), 1)
    }
}

/// The two answer buttons, wired to App Intents. A device-bound answer:
/// endpoint and credentials all come from the activity's attributes. Once
/// the question is settled the pair gives way to its outcome tag.
struct AnswerButtons: View {
    let attributes: HarkActivityAttributes
    let interaction: HarkActivityAttributes.InteractionState
    let accent: Color
    let ink: Color

    var body: some View {
        if interaction.state == "pending" {
            HStack(spacing: 8) {
                Button(intent: intent(for: interaction.primaryAction)) {
                    Text(interaction.primaryLabel)
                }
                .buttonStyle(.instrument(.primary, compact: true, tint: accent, ink: ink))

                Button(intent: intent(for: interaction.secondaryAction)) {
                    Text(interaction.secondaryLabel)
                }
                .buttonStyle(.instrument(.secondary, compact: true))
            }
        } else {
            StateTag(state: interaction.state)
        }
    }

    private func intent(for action: String) -> AnswerActivityIntent {
        let interactionID = attributes.question?.id ?? interaction.id
        let endpoint = HarkResponder.answerEndpoint(
            tokenRegistrationUrl: attributes.tokenRegistrationUrl,
            interactionID: interactionID
        )
        return AnswerActivityIntent(
            endpoint: endpoint?.absoluteString ?? "",
            action: action,
            deviceID: attributes.deviceId,
            actionDigest: attributes.question?.actionDigest ?? "",
            responseToken: attributes.question?.responseToken ?? ""
        )
    }
}

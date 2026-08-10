//
//  ActivityStyleSupport.swift
//  HarkWidgets
//
//  Vocabulary resolution for the content state: symbols, styles, and the
//  pieces every layout shares. Unknown members always fall back — a card
//  that renders plainly beats a card that renders nothing.
//

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

/// The small square glyph plate the progress layouts lead with.
struct SymbolPlate: View {
    let state: HarkActivityAttributes.ContentState
    var size: CGFloat = 36

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .fill(state.accent.opacity(0.16))
            Image(systemName: state.symbolName)
                .font(.system(size: size * 0.44, weight: .semibold))
                .foregroundStyle(state.accent)
        }
        .frame(width: size, height: size)
    }
}

/// A percent readout, monospaced, or nothing when there is no progress.
struct PercentText: View {
    let progress: Double?
    var font: Font = .caption

    var body: some View {
        if let progress {
            Text(clamped(progress), format: .percent.precision(.fractionLength(0)))
                .font(font.weight(.semibold))
                .monospacedDigit()
                .foregroundStyle(Axis.textSecondary)
        }
    }

    private func clamped(_ value: Double) -> Double {
        min(max(value, 0), 1)
    }
}

/// The thin accent progress bar the progress layouts share.
struct AccentProgressBar: View {
    let progress: Double?
    let accent: Color

    var body: some View {
        if let progress {
            ProgressView(value: min(max(progress, 0), 1))
                .progressViewStyle(.linear)
                .tint(accent)
        }
    }
}

/// The settled-answer tag an interactive card shows once its question is no
/// longer pending.
struct AnswerOutcomeTag: View {
    let state: String

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: icon)
                .font(.caption2.weight(.bold))
            Text(label)
                .font(.caption.weight(.semibold))
        }
        .foregroundStyle(tint)
        .padding(.horizontal, 10)
        .padding(.vertical, 5)
        .background(tint.opacity(0.14))
        .clipShape(Capsule())
    }

    private var label: String {
        switch state {
        case "approved": "Approved"
        case "denied": "Denied"
        case "yes": "Yes"
        case "no": "No"
        case "replied": "Replied"
        case "canceled": "Canceled"
        case "expired": "Expired"
        default: state.capitalized
        }
    }

    private var icon: String {
        switch state {
        case "approved", "yes", "replied": "checkmark"
        case "denied", "no": "xmark"
        default: "minus"
        }
    }

    private var tint: Color {
        switch state {
        case "approved", "yes", "replied": Axis.jade
        case "denied", "no": Axis.accent
        default: Axis.textTertiary
        }
    }
}

/// The two answer buttons, wired to App Intents. A device-bound answer:
/// endpoint and credentials all come from the activity's attributes.
struct AnswerButtons: View {
    let attributes: HarkActivityAttributes
    let interaction: HarkActivityAttributes.InteractionState
    let accent: Color
    var compact = false

    var body: some View {
        if interaction.state == "pending" {
            HStack(spacing: 8) {
                Button(intent: intent(for: interaction.primaryAction)) {
                    buttonLabel(interaction.primaryLabel, filled: true)
                }
                .buttonStyle(.plain)

                Button(intent: intent(for: interaction.secondaryAction)) {
                    buttonLabel(interaction.secondaryLabel, filled: false)
                }
                .buttonStyle(.plain)
            }
        } else {
            AnswerOutcomeTag(state: interaction.state)
        }
    }

    private func buttonLabel(_ text: String, filled: Bool) -> some View {
        Text(text)
            .font((compact ? Font.caption : .subheadline).weight(.semibold))
            .lineLimit(1)
            .minimumScaleFactor(0.8)
            .foregroundStyle(filled ? .white : Axis.textPrimary)
            .padding(.horizontal, compact ? 10 : 14)
            .padding(.vertical, compact ? 6 : 9)
            .frame(maxWidth: .infinity)
            .background(filled ? accent : Axis.plateRaised)
            .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 8, style: .continuous)
                    .strokeBorder(filled ? Color.clear : Axis.stroke, lineWidth: 1)
            )
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

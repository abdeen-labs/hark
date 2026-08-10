//
//  AxisComponents.swift
//  Hark
//
//  Reusable Axis pieces: flat plates, chips, pills, section headers.
//  Precise, quiet, dense — a tool, not a toy.
//

import SwiftUI

// MARK: - Plates

struct PlateModifier: ViewModifier {
    var raised = false

    func body(content: Content) -> some View {
        content
            .background(raised ? Axis.plateRaised : Axis.plate)
            .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 10, style: .continuous)
                    .strokeBorder(Axis.stroke, lineWidth: 1)
            )
    }
}

extension View {
    func plate(raised: Bool = false) -> some View {
        modifier(PlateModifier(raised: raised))
    }
}

// MARK: - Section header

struct AxisSectionHeader: View {
    let title: String

    var body: some View {
        Text(title.uppercased())
            .font(.caption2)
            .kerning(1.2)
            .foregroundStyle(Axis.textTertiary)
    }
}

// MARK: - Chips and pills

/// A small label chip: interaction kind, history kind, device capability.
struct AxisChip: View {
    let text: String
    var tint: Color = Axis.textSecondary

    var body: some View {
        Text(text)
            .font(.caption2.weight(.medium))
            .foregroundStyle(tint)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(tint.opacity(0.12))
            .clipShape(Capsule())
    }
}

/// The outcome pill for a settled interaction or a history result.
struct ResultPill: View {
    let result: String

    var body: some View {
        AxisChip(text: label, tint: tint)
    }

    private var label: String {
        switch result {
        case "approved": "Approved"
        case "denied": "Denied"
        case "yes": "Yes"
        case "no": "No"
        case "replied": "Replied"
        case "canceled": "Canceled"
        case "expired": "Expired"
        case "pending": "Pending"
        default: result.capitalized
        }
    }

    private var tint: Color {
        switch result {
        case "approved", "yes", "replied": Axis.jade
        case "denied", "no": Axis.accent
        case "expired", "canceled": Axis.textTertiary
        case "pending": Axis.amber
        default: Axis.textSecondary
        }
    }
}

/// A live countdown to a question's deadline. Monospaced digits.
struct ExpiryCountdown: View {
    let expiresAt: Date

    var body: some View {
        if expiresAt > .now {
            Text(timerInterval: Date.now ... expiresAt, countsDown: true)
                .font(.caption.weight(.medium))
                .monospacedDigit()
                .foregroundStyle(Axis.textSecondary)
        } else {
            Text("Expired")
                .font(.caption.weight(.medium))
                .foregroundStyle(Axis.textTertiary)
        }
    }
}

// MARK: - Kind helpers

enum InteractionKindDisplay {
    static func label(_ kind: String) -> String {
        switch kind {
        case "approval": "Approval"
        case "yes_no": "Yes / No"
        case "reply": "Reply"
        default: kind.capitalized
        }
    }

    static func icon(_ kind: String) -> String {
        switch kind {
        case "approval": "checkmark.seal"
        case "yes_no": "questionmark.circle"
        case "reply": "text.bubble"
        default: "questionmark.circle"
        }
    }
}

// MARK: - Buttons

/// The filled accent button: one per surface, for the primary act.
struct AxisPrimaryButtonStyle: ButtonStyle {
    var tint: Color = Axis.accent

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(.white)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity)
            .background(tint.opacity(configuration.isPressed ? 0.75 : 1))
            .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
    }
}

/// The quiet secondary button: a stroked plate.
struct AxisSecondaryButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(Axis.textPrimary)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity)
            .background(Axis.plateRaised.opacity(configuration.isPressed ? 0.6 : 1))
            .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 8, style: .continuous)
                    .strokeBorder(Axis.stroke, lineWidth: 1)
            )
    }
}

// MARK: - Empty states

struct AxisEmptyState: View {
    let icon: String
    let title: String
    let detail: String

    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: icon)
                .font(.title2)
                .foregroundStyle(Axis.textTertiary)
            Text(title)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(Axis.textSecondary)
            Text(detail)
                .font(.footnote)
                .foregroundStyle(Axis.textTertiary)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 48)
    }
}

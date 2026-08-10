//
//  HarkLiveActivity.swift
//  HarkWidgets
//
//  The Live Activity: Lock Screen card and Dynamic Island, rendering the
//  server's content state verbatim. Five progress styles, four interactive
//  styles; anything unknown renders as standard.
//

import ActivityKit
import SwiftUI
import WidgetKit

struct HarkLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: HarkActivityAttributes.self) { context in
            LockScreenCard(context: context)
                .activityBackgroundTint(Axis.bg)
                .activitySystemActionForegroundColor(context.state.accent)
        } dynamicIsland: { context in
            let state = context.state
            let accent = state.accent
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Image(systemName: state.symbolName)
                        .font(.title3)
                        .foregroundStyle(accent)
                        .padding(.leading, 4)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    PercentText(progress: state.progress, font: .subheadline)
                        .padding(.trailing, 4)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    IslandBottom(context: context)
                }
            } compactLeading: {
                Image(systemName: state.symbolName)
                    .foregroundStyle(accent)
            } compactTrailing: {
                if let progress = state.progress {
                    ProgressView(value: min(max(progress, 0), 1))
                        .progressViewStyle(.circular)
                        .tint(accent)
                } else if state.interaction != nil {
                    Image(systemName: "questionmark")
                        .foregroundStyle(accent)
                }
            } minimal: {
                Image(systemName: state.symbolName)
                    .foregroundStyle(accent)
            }
            .keylineTint(accent)
        }
    }
}

// MARK: - Lock Screen

private struct LockScreenCard: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        Group {
            switch ActivityStyleKind.resolve(state) {
            case .standard:
                StandardCard(state: state)
            case .ring:
                RingCard(state: state)
            case .hero:
                HeroCard(state: state)
            case .terminal:
                TerminalCard(state: state)
            case .steps:
                StepsCard(state: state)
            case .approval:
                ApprovalCard(context: context)
            case .shell:
                ShellCard(context: context)
            case .verdict:
                VerdictCard(context: context)
            case .signal:
                SignalCard(context: context)
            }
        }
        .padding(14)
        .opacity(context.isStale ? 0.7 : 1)
    }
}

// MARK: - Progress styles

/// standard: glyph plate, title over status, percent, thin bar, detail.
private struct StandardCard: View {
    let state: HarkActivityAttributes.ContentState

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                SymbolPlate(state: state)
                VStack(alignment: .leading, spacing: 1) {
                    Text(state.title)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(Axis.textPrimary)
                        .lineLimit(1)
                        .harkMasked(state.isPrivate)
                    Text(state.status)
                        .font(.caption)
                        .foregroundStyle(Axis.textSecondary)
                        .lineLimit(1)
                }
                Spacer()
                PercentText(progress: state.progress, font: .subheadline)
            }
            AccentProgressBar(progress: state.progress, accent: state.accent)
            if let detail = state.detail {
                Text(detail)
                    .font(.caption)
                    .foregroundStyle(Axis.textTertiary)
                    .lineLimit(2)
                    .harkMasked(state.isPrivate)
            }
        }
    }
}

/// ring: a circular gauge carries the progress; text sits beside it.
private struct RingCard: View {
    let state: HarkActivityAttributes.ContentState

    var body: some View {
        HStack(spacing: 14) {
            Gauge(value: min(max(state.progress ?? 0, 0), 1)) {
                EmptyView()
            } currentValueLabel: {
                if state.progress != nil {
                    Text(min(max(state.progress ?? 0, 0), 1), format: .percent.precision(.fractionLength(0)))
                        .font(.caption2.weight(.semibold))
                        .monospacedDigit()
                } else {
                    Image(systemName: state.symbolName)
                        .font(.caption)
                }
            }
            .gaugeStyle(.accessoryCircularCapacity)
            .tint(state.accent)
            .frame(width: 52, height: 52)

            VStack(alignment: .leading, spacing: 2) {
                Text(state.title)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Axis.textPrimary)
                    .lineLimit(1)
                    .harkMasked(state.isPrivate)
                Text(state.status)
                    .font(.caption)
                    .foregroundStyle(Axis.textSecondary)
                    .lineLimit(1)
                if let detail = state.detail {
                    Text(detail)
                        .font(.caption2)
                        .foregroundStyle(Axis.textTertiary)
                        .lineLimit(2)
                        .harkMasked(state.isPrivate)
                }
            }
            Spacer(minLength: 0)
        }
    }
}

/// hero: the title is the card.
private struct HeroCard: View {
    let state: HarkActivityAttributes.ContentState

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: state.symbolName)
                    .font(.subheadline)
                    .foregroundStyle(state.accent)
                Spacer()
                PercentText(progress: state.progress, font: .title3)
            }
            Text(state.title)
                .font(.title3.weight(.bold))
                .foregroundStyle(Axis.textPrimary)
                .lineLimit(2)
                .harkMasked(state.isPrivate)
            Text(state.detail.map { "\(state.status) — \($0)" } ?? state.status)
                .font(.caption)
                .foregroundStyle(Axis.textSecondary)
                .lineLimit(2)
                .harkMasked(state.isPrivate && state.detail != nil)
            AccentProgressBar(progress: state.progress, accent: state.accent)
        }
    }
}

/// terminal: monospace, prompt-marked, quiet.
private struct TerminalCard: View {
    let state: HarkActivityAttributes.ContentState

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Text("❯")
                    .font(.caption.monospaced().weight(.bold))
                    .foregroundStyle(state.accent)
                Text(state.title)
                    .font(.caption.monospaced())
                    .foregroundStyle(Axis.textSecondary)
                    .lineLimit(1)
                    .harkMasked(state.isPrivate)
                Spacer()
                PercentText(progress: state.progress, font: .caption.monospaced())
            }
            Text(state.status)
                .font(.subheadline.monospaced().weight(.medium))
                .foregroundStyle(Axis.textPrimary)
                .lineLimit(2)
            if let detail = state.detail {
                Text(detail)
                    .font(.caption.monospaced())
                    .foregroundStyle(Axis.textTertiary)
                    .lineLimit(2)
                    .harkMasked(state.isPrivate)
            }
            AccentProgressBar(progress: state.progress, accent: state.accent)
        }
    }
}

/// steps: progress rendered as discrete segments.
private struct StepsCard: View {
    let state: HarkActivityAttributes.ContentState

    private static let segments = 6

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                SymbolPlate(state: state, size: 30)
                VStack(alignment: .leading, spacing: 1) {
                    Text(state.title)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(Axis.textPrimary)
                        .lineLimit(1)
                        .harkMasked(state.isPrivate)
                    Text(state.status)
                        .font(.caption)
                        .foregroundStyle(Axis.textSecondary)
                        .lineLimit(1)
                }
                Spacer()
                if let detail = state.detail {
                    Text(detail)
                        .font(.caption.weight(.medium))
                        .monospacedDigit()
                        .foregroundStyle(Axis.textSecondary)
                        .lineLimit(1)
                        .harkMasked(state.isPrivate)
                }
            }
            HStack(spacing: 4) {
                ForEach(0 ..< Self.segments, id: \.self) { index in
                    RoundedRectangle(cornerRadius: 2, style: .continuous)
                        .fill(index < filledSegments ? state.accent : Axis.plateRaised)
                        .frame(height: 5)
                }
            }
        }
    }

    private var filledSegments: Int {
        guard let progress = state.progress else { return 0 }
        return Int((min(max(progress, 0), 1) * Double(Self.segments)).rounded())
    }
}

// MARK: - Interactive styles

/// approval: header, prompt, two buttons. The default question card.
private struct ApprovalCard: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: state.symbolName)
                    .font(.caption)
                    .foregroundStyle(state.accent)
                Text(state.title)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Axis.textSecondary)
                    .lineLimit(1)
                    .harkMasked(state.isPrivate)
                Spacer()
                Text(state.status)
                    .font(.caption2)
                    .foregroundStyle(Axis.textTertiary)
            }
            if let interaction = state.interaction {
                Text(interaction.prompt)
                    .font(.subheadline)
                    .foregroundStyle(Axis.textPrimary)
                    .lineLimit(3)
                    .harkMasked(state.isPrivate)
                AnswerButtons(attributes: context.attributes, interaction: interaction, accent: state.accent)
            }
        }
    }
}

/// shell: the question as a terminal prompt.
private struct ShellCard: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Text("❯")
                    .font(.caption.monospaced().weight(.bold))
                    .foregroundStyle(state.accent)
                Text(state.title)
                    .font(.caption.monospaced())
                    .foregroundStyle(Axis.textSecondary)
                    .lineLimit(1)
                    .harkMasked(state.isPrivate)
                Spacer()
            }
            if let interaction = state.interaction {
                Text(interaction.prompt)
                    .font(.subheadline.monospaced())
                    .foregroundStyle(Axis.textPrimary)
                    .lineLimit(3)
                    .harkMasked(state.isPrivate)
                AnswerButtons(
                    attributes: context.attributes,
                    interaction: interaction,
                    accent: state.accent,
                    compact: true
                )
            }
        }
    }
}

/// verdict: the prompt, centered and large, over two equal buttons.
private struct VerdictCard: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        VStack(spacing: 12) {
            if let interaction = state.interaction {
                Text(interaction.prompt)
                    .font(.headline)
                    .foregroundStyle(Axis.textPrimary)
                    .multilineTextAlignment(.center)
                    .lineLimit(3)
                    .frame(maxWidth: .infinity)
                    .harkMasked(state.isPrivate)
                Text(state.title)
                    .font(.caption2)
                    .foregroundStyle(Axis.textTertiary)
                    .harkMasked(state.isPrivate)
                AnswerButtons(attributes: context.attributes, interaction: interaction, accent: state.accent)
            }
        }
    }
}

/// signal: compact — a warning glyph, the prompt, tight buttons.
private struct SignalCard: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        HStack(spacing: 12) {
            SymbolPlate(state: state, size: 40)
            VStack(alignment: .leading, spacing: 6) {
                if let interaction = state.interaction {
                    Text(interaction.prompt)
                        .font(.footnote.weight(.medium))
                        .foregroundStyle(Axis.textPrimary)
                        .lineLimit(2)
                        .harkMasked(state.isPrivate)
                    AnswerButtons(
                        attributes: context.attributes,
                        interaction: interaction,
                        accent: state.accent,
                        compact: true
                    )
                }
            }
            Spacer(minLength: 0)
        }
    }
}

// MARK: - Dynamic Island bottom region

private struct IslandBottom: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        VStack(alignment: .leading, spacing: 6) {
            if let interaction = state.interaction {
                Text(interaction.prompt)
                    .font(.footnote)
                    .foregroundStyle(Axis.textPrimary)
                    .lineLimit(2)
                AnswerButtons(
                    attributes: context.attributes,
                    interaction: interaction,
                    accent: state.accent,
                    compact: true
                )
            } else {
                Text(state.title)
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Axis.textPrimary)
                    .lineLimit(1)
                Text(state.detail.map { "\(state.status) — \($0)" } ?? state.status)
                    .font(.caption)
                    .foregroundStyle(Axis.textSecondary)
                    .lineLimit(1)
                AccentProgressBar(progress: state.progress, accent: state.accent)
            }
        }
        .padding(.horizontal, 4)
    }
}

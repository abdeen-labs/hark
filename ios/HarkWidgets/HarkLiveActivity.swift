//
//  HarkLiveActivity.swift
//  HarkWidgets
//
//  The Live Activity: Lock Screen card and Dynamic Island, rendering the
//  server's content state verbatim. Five progress styles, four interactive
//  styles; anything unknown renders as standard. Same paper, same ink, same
//  instrument controls as the app.
//

import ActivityKit
import SwiftUI
import WidgetKit

struct HarkLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: HarkActivityAttributes.self) { context in
            LockScreenCard(context: context)
                .activityBackgroundTint(Axis.paper)
                .activitySystemActionForegroundColor(context.state.accent)
        } dynamicIsland: { context in
            let state = context.state
            let accent = state.accent
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Image(systemName: state.symbolName)
                        .font(.system(size: 18, weight: .semibold))
                        .foregroundStyle(accent)
                        .padding(.leading, 4)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    PercentText(progress: state.progress, size: 14)
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
                    Rectangle()
                        .fill(accent)
                        .frame(width: 6, height: 6)
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

/// standard: glyph plate, title over status, percent, the thin rule, detail.
private struct StandardCard: View {
    let state: HarkActivityAttributes.ContentState

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                SymbolPlate(state: state)
                VStack(alignment: .leading, spacing: 2) {
                    Text(state.title)
                        .font(AxisType.copy(14, weight: .semibold))
                        .foregroundStyle(Axis.ink)
                        .lineLimit(1)
                        .harkMasked(state.isPrivate)
                    Text(state.status)
                        .font(AxisType.mono(11))
                        .foregroundStyle(Axis.inkSubtle)
                        .lineLimit(1)
                }
                Spacer()
                PercentText(progress: state.progress)
            }
            ThinBar(progress: state.progress, tint: state.accent)
            if let detail = state.detail {
                Text(detail)
                    .font(AxisType.copy(12))
                    .foregroundStyle(Axis.inkFaint)
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
                        .font(AxisType.mono(10, weight: .semibold))
                        .monospacedDigit()
                } else {
                    Image(systemName: state.symbolName)
                        .font(.system(size: 12, weight: .semibold))
                }
            }
            .gaugeStyle(.accessoryCircularCapacity)
            .tint(state.accent)
            .frame(width: 52, height: 52)

            VStack(alignment: .leading, spacing: 3) {
                Text(state.title)
                    .font(AxisType.copy(14, weight: .semibold))
                    .foregroundStyle(Axis.ink)
                    .lineLimit(1)
                    .harkMasked(state.isPrivate)
                Text(state.status)
                    .font(AxisType.mono(11))
                    .foregroundStyle(Axis.inkSubtle)
                    .lineLimit(1)
                if let detail = state.detail {
                    Text(detail)
                        .font(AxisType.copy(12))
                        .foregroundStyle(Axis.inkFaint)
                        .lineLimit(2)
                        .harkMasked(state.isPrivate)
                }
            }
            Spacer(minLength: 0)
        }
    }
}

/// hero: the title is the card; the percent sits against it at scale.
private struct HeroCard: View {
    let state: HarkActivityAttributes.ContentState

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                HStack(spacing: 6) {
                    StatusLight(color: state.accent, size: 5)
                    Meta(state.status, color: Axis.inkSubtle)
                }
                Spacer()
                PercentText(progress: state.progress, size: 22)
            }
            Text(state.title)
                .axisHeadline(22)
                .foregroundStyle(Axis.ink)
                .lineLimit(2)
                .harkMasked(state.isPrivate)
            if let detail = state.detail {
                Text(detail)
                    .font(AxisType.copy(12))
                    .foregroundStyle(Axis.inkFaint)
                    .lineLimit(1)
                    .harkMasked(state.isPrivate)
            }
            ThinBar(progress: state.progress, tint: state.accent)
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
                    .font(AxisType.mono(11, weight: .bold))
                    .foregroundStyle(state.accent)
                Text(state.title)
                    .font(AxisType.mono(11))
                    .foregroundStyle(Axis.inkSubtle)
                    .lineLimit(1)
                    .harkMasked(state.isPrivate)
                Spacer()
                PercentText(progress: state.progress, size: 11, color: Axis.inkSubtle)
            }
            Text(state.status)
                .font(AxisType.mono(14, weight: .medium))
                .foregroundStyle(Axis.ink)
                .lineLimit(2)
            if let detail = state.detail {
                Text(detail)
                    .font(AxisType.mono(11))
                    .foregroundStyle(Axis.inkFaint)
                    .lineLimit(2)
                    .harkMasked(state.isPrivate)
            }
            ThinBar(progress: state.progress, tint: state.accent)
                .padding(.top, 4)
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
                VStack(alignment: .leading, spacing: 2) {
                    Text(state.title)
                        .font(AxisType.copy(14, weight: .semibold))
                        .foregroundStyle(Axis.ink)
                        .lineLimit(1)
                        .harkMasked(state.isPrivate)
                    Text(state.status)
                        .font(AxisType.mono(11))
                        .foregroundStyle(Axis.inkSubtle)
                        .lineLimit(1)
                }
                Spacer()
                if let detail = state.detail {
                    Text(detail)
                        .font(AxisType.mono(11, weight: .medium))
                        .monospacedDigit()
                        .foregroundStyle(Axis.inkSubtle)
                        .lineLimit(1)
                        .harkMasked(state.isPrivate)
                }
            }
            HStack(spacing: 3) {
                ForEach(0 ..< Self.segments, id: \.self) { index in
                    Rectangle()
                        .fill(index < filledSegments ? state.accent : Axis.surface3)
                        .frame(height: 4)
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

/// approval: header, prompt, two controls. The default question card.
private struct ApprovalCard: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                StatusLight(color: state.accent, size: 5)
                Meta(state.title, color: Axis.inkSubtle)
                    .harkMasked(state.isPrivate)
                Spacer()
                Meta(state.status)
            }
            if let interaction = state.interaction {
                Text(interaction.prompt)
                    .font(AxisType.copy(14))
                    .foregroundStyle(Axis.ink)
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
                    .font(AxisType.mono(11, weight: .bold))
                    .foregroundStyle(state.accent)
                Text(state.title)
                    .font(AxisType.mono(11))
                    .foregroundStyle(Axis.inkSubtle)
                    .lineLimit(1)
                    .harkMasked(state.isPrivate)
                Spacer()
            }
            if let interaction = state.interaction {
                Text(interaction.prompt)
                    .font(AxisType.mono(13))
                    .foregroundStyle(Axis.ink)
                    .lineLimit(3)
                    .harkMasked(state.isPrivate)
                AnswerButtons(attributes: context.attributes, interaction: interaction, accent: state.accent)
            }
        }
    }
}

/// verdict: the prompt, centered and large, over two equal controls.
private struct VerdictCard: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        VStack(spacing: 12) {
            if let interaction = state.interaction {
                Text(interaction.prompt)
                    .axisHeadline(17)
                    .foregroundStyle(Axis.ink)
                    .multilineTextAlignment(.center)
                    .lineLimit(3)
                    .frame(maxWidth: .infinity)
                    .harkMasked(state.isPrivate)
                Meta(state.title)
                    .harkMasked(state.isPrivate)
                AnswerButtons(attributes: context.attributes, interaction: interaction, accent: state.accent)
            }
        }
    }
}

/// signal: compact — the glyph plate, the prompt, tight controls.
private struct SignalCard: View {
    let context: ActivityViewContext<HarkActivityAttributes>

    var body: some View {
        let state = context.state
        HStack(spacing: 12) {
            SymbolPlate(state: state, size: 40)
            VStack(alignment: .leading, spacing: 8) {
                if let interaction = state.interaction {
                    Text(interaction.prompt)
                        .font(AxisType.copy(13, weight: .medium))
                        .foregroundStyle(Axis.ink)
                        .lineLimit(2)
                        .harkMasked(state.isPrivate)
                    AnswerButtons(attributes: context.attributes, interaction: interaction, accent: state.accent)
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
                    .font(AxisType.copy(13))
                    .foregroundStyle(Axis.ink)
                    .lineLimit(2)
                AnswerButtons(attributes: context.attributes, interaction: interaction, accent: state.accent)
            } else {
                Text(state.title)
                    .font(AxisType.copy(13, weight: .semibold))
                    .foregroundStyle(Axis.ink)
                    .lineLimit(1)
                Text(state.detail.map { "\(state.status) — \($0)" } ?? state.status)
                    .font(AxisType.mono(11))
                    .foregroundStyle(Axis.inkSubtle)
                    .lineLimit(1)
                ThinBar(progress: state.progress, tint: state.accent)
            }
        }
        .padding(.horizontal, 4)
    }
}

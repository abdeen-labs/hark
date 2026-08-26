//
//  AxisControls.swift
//  Hark
//
//  Toggle and choice controls used by settings forms.
//

import SwiftUI

/// A labeled toggle with optional supporting text.
struct AxisToggle: View {
    let label: String
    var sub: String?
    var compact = false
    var busy = false
    var disabled = false
    let isOn: Bool
    let action: (Bool) -> Void

    init(
        _ label: String,
        sub: String? = nil,
        compact: Bool = false,
        busy: Bool = false,
        disabled: Bool = false,
        isOn: Bool,
        action: @escaping (Bool) -> Void
    ) {
        self.label = label
        self.sub = sub
        self.compact = compact
        self.busy = busy
        self.disabled = disabled
        self.isOn = isOn
        self.action = action
    }

    var body: some View {
        Button {
            action(!isOn)
        } label: {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(label)
                        .font(AxisType.copy(compact ? 13 : 15, weight: .medium))
                        .foregroundStyle(Axis.ink)
                        .lineLimit(1)
                    if let sub {
                        Text(sub)
                            .font(AxisType.copy(12))
                            .foregroundStyle(Axis.inkFaint)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: 12)
                Meta(isOn ? "On" : "Off", size: 9, color: isOn ? Axis.signalText : Axis.inkFaint)
                track
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(busy || disabled)
        .opacity(disabled ? 0.55 : 1)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue(isOn ? "On" : "Off")
        .accessibilityAddTraits(.isToggle)
    }

    private var track: some View {
        let width: CGFloat = compact ? 34 : 40
        let height: CGFloat = compact ? 20 : 22
        let inset: CGFloat = compact ? 3 : 4
        return ZStack(alignment: isOn ? .trailing : .leading) {
            Rectangle()
                .fill(isOn ? Axis.signalWash : Axis.field)
            Rectangle()
                .fill(isOn ? Axis.signal : Axis.inkSubtle)
                .frame(width: 14, height: 14)
                .padding(inset)
                .opacity(busy ? 0.45 : 1)
        }
        .frame(width: width, height: height)
        .overlay(Rectangle().strokeBorder(isOn ? Axis.signalLine : Axis.lineStrong, lineWidth: 1))
        .animation(Axis.Motion.quick, value: isOn)
    }
}

/// A wrapping single-select group.
struct AxisChoiceChips: View {
    let options: [(value: String, label: String)]
    @Binding var selection: String?

    var body: some View {
        FlowRow(spacing: 8, lineSpacing: 8) {
            ForEach(options, id: \.value) { option in
                chip(value: option.value, label: option.label)
            }
        }
    }

    private func chip(value: String, label: String) -> some View {
        let selected = selection == value
        return Button {
            withAnimation(Axis.Motion.quick) { selection = value }
        } label: {
            HStack(spacing: 6) {
                if selected {
                    Rectangle()
                        .fill(Axis.signalText)
                        .frame(width: 5, height: 5)
                }
                Text(label)
                    .axisMeta(10)
                    .textCase(.uppercase)
                    .lineLimit(1)
            }
            .foregroundStyle(selected ? Axis.signalText : Axis.inkSubtle)
            .padding(.horizontal, 10)
            .frame(minHeight: 32)
            .background(selected ? Axis.signalWash : Color.clear)
            .overlay(
                RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous)
                    .strokeBorder(selected ? Axis.signalLine : Axis.lineStrong, lineWidth: 1)
            )
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? [.isSelected] : [])
    }
}

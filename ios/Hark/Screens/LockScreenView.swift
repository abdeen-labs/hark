//
//  LockScreenView.swift
//  Hark
//

import SwiftUI

struct LockScreenView: View {
    @Environment(AppLock.self) private var lock

    var body: some View {
        ZStack {
            Axis.paper.ignoresSafeArea()
            DotMatrix().ignoresSafeArea()
            VStack(alignment: .leading, spacing: 0) {
                LockBrand()
                    .padding(.top, 24)
                Spacer(minLength: 36)
                panel
                LockFooter()
                    .padding(.top, 40)
                    .padding(.bottom, 24)
            }
            .padding(.horizontal, Axis.gutter)
        }
    }

    private var panel: some View {
        Module(label: "Locked", variant: .marked) {
            VStack(alignment: .leading, spacing: 20) {
                HStack(alignment: .firstTextBaseline, spacing: 14) {
                    Image(systemName: "faceid")
                        .font(.system(size: 26, weight: .medium))
                        .foregroundStyle(Axis.signalText)
                        .accessibilityHidden(true)
                    Text("Use Face ID or your passcode to unlock Hark.")
                        .font(AxisType.copy(14))
                        .lineSpacing(3)
                        .foregroundStyle(Axis.inkSubtle)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Button(lock.isPrompting ? "Checking…" : "Unlock") {
                    Task { await lock.unlock() }
                }
                .buttonStyle(.instrument(.primary))
                .disabled(lock.isPrompting)
            }
        } trailing: {
            HStack(spacing: 8) {
                StatusLight(color: Axis.signal, size: 5)
                Meta("Locked", color: Axis.inkSubtle)
            }
        }
    }
}

struct PrivacyShieldView: View {
    private static let rows: [(title: CGFloat, detail: CGFloat, opacity: Double)] = [
        (0.34, 0.62, 1),
        (0.28, 0.78, 0.72),
        (0.42, 0.55, 0.48),
        (0.30, 0.70, 0.26),
    ]

    var body: some View {
        ZStack {
            Axis.paper.ignoresSafeArea()
            DotMatrix().ignoresSafeArea()
            VStack(alignment: .leading, spacing: 0) {
                LockBrand()
                    .padding(.top, 24)
                Spacer(minLength: 32)
                ledger
                Spacer(minLength: 32)
                LockFooter()
                    .padding(.bottom, 24)
            }
            .padding(.horizontal, Axis.gutter)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Hark content is hidden")
    }

    private var ledger: some View {
        Module(label: "Content hidden", variant: .marked, flush: true) {
            VStack(spacing: 0) {
                ForEach(Array(Self.rows.enumerated()), id: \.offset) { index, row in
                    RedactionRow(number: index + 1, title: row.title, detail: row.detail)
                        .opacity(row.opacity)
                    if index < Self.rows.count - 1 {
                        Hairline(color: Axis.lineFaint)
                    }
                }
            }
        } trailing: {
            HStack(spacing: 8) {
                StatusLight(color: Axis.signal, size: 5)
                Meta("Private", color: Axis.inkSubtle)
            }
        }
        .accessibilityHidden(true)
    }
}

private struct RedactionRow: View {
    let number: Int
    let title: CGFloat
    let detail: CGFloat

    var body: some View {
        HStack(alignment: .top, spacing: 0) {
            LedgerIndex(number: number)
                .padding(.top, 1)
            GeometryReader { proxy in
                VStack(alignment: .leading, spacing: 9) {
                    bar(width: proxy.size.width * title, color: Axis.surface3)
                    bar(width: proxy.size.width * detail, color: Axis.surface2)
                }
            }
            .frame(height: 27)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 14)
    }

    private func bar(width: CGFloat, color: Color) -> some View {
        RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous)
            .fill(color)
            .overlay(
                RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous)
                    .strokeBorder(Axis.lineFaint, lineWidth: 1)
            )
            .frame(width: width, height: 9)
    }
}

private struct LockBrand: View {
    private static let titleSize: CGFloat = 96

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack(spacing: 8) {
                IndexLabel("Hark")
                Meta("Push relay")
            }
            Text("Hark")
                .axisDisplay(Self.titleSize)
                .foregroundStyle(Axis.ink)
                .lineLimit(1)
                .fixedSize()
                .padding(.top, -Self.titleSize * 0.2)
                .padding(.bottom, -Self.titleSize * 0.14)
                .offset(x: -Self.titleSize * 0.05)
                .accessibilityAddTraits(.isHeader)
        }
    }
}

private struct LockFooter: View {
    var body: some View {
        HStack {
            Lockup()
            Spacer()
            Meta("Hark · Rev \(AppInfo.version)", color: Axis.inkFaint)
        }
        .padding(.top, 14)
        .overlay(alignment: .top) { Hairline() }
    }
}

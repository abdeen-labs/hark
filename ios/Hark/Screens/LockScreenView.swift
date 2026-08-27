//
//  LockScreenView.swift
//  Hark
//
//  The lock veil and the app-switcher shield. The veil is the gate drawn
//  over the whole app while it is locked; the shield is the same paper with
//  nothing on it but the name, for the snapshot the system keeps.
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
                    Text("Hark is locked. Face ID or your passcode opens it.")
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
                Meta("Secured", color: Axis.inkSubtle)
            }
        }
    }
}

/// The paper with nothing on it but the name — what the app switcher keeps
/// while Hark is in the background.
struct PrivacyShieldView: View {
    var body: some View {
        ZStack {
            Axis.paper.ignoresSafeArea()
            DotMatrix().ignoresSafeArea()
            VStack(alignment: .leading, spacing: 0) {
                LockBrand()
                    .padding(.top, 24)
                Spacer()
                LockFooter()
                    .padding(.bottom, 24)
            }
            .padding(.horizontal, Axis.gutter)
        }
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
        HStack(alignment: .firstTextBaseline) {
            Text("Abdeen Labs")
                .font(AxisType.meta(11))
                .tracking(AxisType.tracking(AxisType.wordmarkTracking, at: 11))
                .textCase(.uppercase)
                .foregroundStyle(Axis.inkFaint)
            Spacer()
            Meta("Hark · Rev \(AppInfo.version)", color: Axis.inkFaint)
        }
        .padding(.top, 14)
        .overlay(alignment: .top) { Hairline() }
    }
}

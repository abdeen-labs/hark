//
//  AxisShell.swift
//  Hark
//
//  The signed-in chrome: a mast along the top carrying the brand, the
//  product's role and the device's registration state; the column ruler
//  under it; and the section rail along the bottom, indexed the way the
//  dashboard's navigation is. No floating bars, no glass — rules and type.
//

import SwiftUI

// MARK: - Mast

struct Mast: View {
    @Environment(AppModel.self) private var model

    /// The bar, its rule, and the column ruler.
    static let height: CGFloat = 44 + 1 + 6

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Rectangle()
                    .fill(Axis.signal)
                    .frame(width: 10, height: 10)
                Text("Hark")
                    .font(.system(size: 15, weight: .semibold))
                    .tracking(-0.3)
                    .foregroundStyle(Axis.ink)
                Hairline(vertical: true)
                    .frame(height: 14)
                Meta("Push relay")
                Spacer(minLength: 12)
                HStack(spacing: 8) {
                    StatusLight(color: registered ? Axis.ok : Axis.warn, size: 5, blinking: !registered)
                    Meta(registered ? "Registered" : "Registering", color: Axis.inkSubtle)
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(registered ? "Device registered" : "Device registering")
            }
            .padding(.horizontal, Axis.gutter)
            .frame(height: 44)
            Hairline()
            ColumnRuler(height: 6)
        }
        .background(Axis.paper.ignoresSafeArea(edges: .top))
    }

    private var registered: Bool {
        model.deviceID != nil
    }
}

// MARK: - Rail

struct Rail: View {
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    static let itemHeight: CGFloat = 56
    /// The rule and the row of sections.
    static let height: CGFloat = 1 + itemHeight

    private static let sections: [(tab: AppModel.Tab, index: String, label: String)] = [
        (.inbox, "01", "Inbox"),
        (.history, "02", "History"),
        (.devices, "03", "Devices"),
        (.settings, "04", "Settings"),
    ]

    var body: some View {
        VStack(spacing: 0) {
            Hairline()
            HStack(spacing: 0) {
                ForEach(Self.sections, id: \.tab) { section in
                    item(section.tab, index: section.index, label: section.label)
                }
            }
            .padding(.horizontal, Axis.gutter - 12)
        }
        .background(Axis.paper.ignoresSafeArea(edges: .bottom))
    }

    private func item(_ tab: AppModel.Tab, index: String, label: String) -> some View {
        let active = model.selectedTab == tab
        let pending = tab == .inbox ? model.inbox.count : 0
        return Button {
            withAnimation(reduceMotion ? nil : Axis.Motion.ease) {
                model.selectedTab = tab
            }
        } label: {
            VStack(alignment: .leading, spacing: 4) {
                IndexLabel(index, color: active ? Axis.signalText : Axis.inkDisabled)
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(label)
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(active ? Axis.ink : Axis.inkSubtle)
                    if pending > 0 {
                        Text(String(format: "%02d", pending))
                            .font(AxisType.meta(10))
                            .monospacedDigit()
                            .foregroundStyle(Axis.signalText)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 12)
            .frame(height: Self.itemHeight)
            .overlay(alignment: .top) {
                Rectangle()
                    .fill(Axis.signal)
                    .frame(height: 2)
                    .padding(.horizontal, 12)
                    .scaleEffect(x: active ? 1 : 0, anchor: .leading)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(pending > 0 ? "\(label), \(pending) pending" : label)
        .accessibilityAddTraits(active ? [.isSelected] : [])
    }
}

// MARK: - Shell

/// The four sections, all resident so each keeps its scroll position and its
/// loaded state; only the selected one is visible and hit-testable. The mast
/// and the rail are fixed overlays: a navigation stack resolves its own safe
/// area from UIKit and ignores insets placed outside it, so each page insets
/// itself with `shellInsets()` from inside the stack instead.
struct RootTabView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ZStack {
            section(.inbox) { InboxView() }
            section(.history) { HistoryView() }
            section(.devices) { DevicesView() }
            section(.settings) { SettingsView() }
        }
        .overlay(alignment: .top) { Mast() }
        .overlay(alignment: .bottom) { Rail() }
    }

    private func section<Content: View>(_ tab: AppModel.Tab, @ViewBuilder content: () -> Content) -> some View {
        let active = model.selectedTab == tab
        return content()
            .opacity(active ? 1 : 0)
            .allowsHitTesting(active)
            .accessibilityHidden(!active)
    }
}

extension View {
    /// Keeps a page's content clear of the mast and the rail. Applied inside
    /// every navigation stack, on each page and on each pushed destination.
    func shellInsets() -> some View {
        safeAreaPadding(.top, Mast.height)
            .safeAreaPadding(.bottom, Rail.height)
    }
}

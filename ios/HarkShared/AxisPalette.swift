//
//  AxisPalette.swift
//  Hark
//

import SwiftUI
import UIKit

nonisolated enum Axis {
    // MARK: Grounds

    static let paper = Color(axisDark: axisVoidRGB, light: axisMistRGB)
    static let surface = Color(axisDark: 0x121827, light: 0xEAEEF5)
    static let surface2 = Color(axisDark: 0x192133, light: 0xDEE4F2)
    static let surface3 = Color(axisDark: 0x212A40, light: 0xD2D9EB)
    static let field = surface2

    // MARK: Ink

    static let ink = Color(axisDark: axisChalkRGB, light: axisCarbonRGB)
    static let inkMuted = Color(axisDark: 0xDBE2F4, light: 0x1E2537)
    static let inkSubtle = Color(axisDark: 0xAFB9CF, light: 0x3C455A)
    static let inkFaint = Color(axisDark: 0x939FBD, light: 0x3C455A)
    /// Decorative only — indexes and ledger numbers hidden from assistive
    /// technology. It does not meet AA against the paper.
    static let inkDisabled = Color(axisDark: 0x555E75, light: 0x788197)

    // MARK: Rules

    static let line = Color(axisDark: 0x313C5A, light: 0xBBC5DB)
    static let lineStrong = Color(axisDark: 0x3A4769, light: 0xB1BBD2)
    static let lineFaint = Color(axisDark: 0x212A40, light: 0xD2D9EB)

    // MARK: Signal

    /// Lines, strips, rules, and lights. Not small text.
    static let signal = Color(axisDark: axisAccentRGB, light: axisAccentDeepRGB)
    /// The filled scarlet field. Its label is `onSignal`.
    static let signalField = Color(axisRGB: axisAccentRGB)
    /// The scarlet under a label the system draws in white.
    static let signalDeep = Color(axisDark: axisAccentDeepRGB, light: axisAccentInkRGB)
    /// The signal colour at text weight; passes AA on the paper.
    static let signalText = Color(axisDark: axisAccentRGB, light: axisAccentInkRGB)
    /// Carbon ink on any filled scarlet, alarm, or highlighter field.
    static let onField = Color(axisRGB: axisCarbonRGB)
    static let onSignal = onField
    static let signalWash = signal.opacity(0.12)
    static let signalLine = Color(axisRGB: axisAccentDeepRGB)

    // MARK: States

    static let ok = Color(axisDark: 0x5AA7FF, light: 0x1D5A96)
    static let okLine = ok.opacity(0.5)
    /// A warning's line and label. Never a solid frame.
    static let warn = Color(axisDark: 0xF5FF00, light: 0x766800)
    /// The highlighter chip fill, under `onField`, on either ground.
    static let warnChip = Color(axisRGB: 0xF5FF00)
    /// An alarm's hatch, strike, pulse, and label. Never a solid line.
    static let alarm = Color(axisDark: 0xFF2BD6, light: 0xBF0099)
    /// The filled alarm field, under `onField`, on either ground.
    static let alarmField = Color(axisRGB: 0xFF2BD6)

    /// Fallback for invalid or low-contrast Live Activity accents.
    static let accent = signalText

    // MARK: Geometry

    enum Radius {
        static let xs: CGFloat = 2
        static let sm: CGFloat = 3
        static let md: CGFloat = 6
        static let lg: CGFloat = 10
    }

    /// The phone's gutter and column gap: four columns under a 16 pt gutter.
    static let gutter: CGFloat = 16
    static let gap: CGFloat = 12
    static let columns = 4

    // MARK: Motion

    enum Motion {
        /// Colour and opacity feedback on frequent interactions.
        static let state: Double = 0.12
        /// A small directional shift.
        static let shift: Double = 0.17
        /// An infrequent staged entrance.
        static let enter: Double = 0.52
        /// The scale a control compresses to while pressed.
        static let press: CGFloat = 0.96
        static var ease: Animation { .timingCurve(0.2, 0, 0, 1, duration: shift) }
        static var quick: Animation { .timingCurve(0.2, 0, 0, 1, duration: state) }
    }
}

nonisolated private let axisVoidRGB: UInt32 = 0x0A0F1C
nonisolated private let axisMistRGB: UInt32 = 0xF0F3FA
nonisolated private let axisChalkRGB: UInt32 = 0xF3F7FF
nonisolated private let axisCarbonRGB: UInt32 = 0x0A0F1C

nonisolated private let axisAccentRGB: UInt32 = 0xFE002A
nonisolated private let axisAccentDeepRGB: UInt32 = 0xD4212C
nonisolated private let axisAccentInkRGB: UInt32 = 0xBE0018

/// The floor an accent has to clear against the paper it is drawn on: the
/// 3:1 of WCAG's non-text contrast, since the accent carries glyphs, bars,
/// and keylines rather than body copy.
nonisolated private let axisAccentFloor: Double = 3

/// The floor a label has to clear against the field it sits on.
nonisolated private let axisLabelFloor: Double = 4.5

/// A `#RRGGBB` string as channel values in 0…1, or nil when the string is
/// not that form.
nonisolated private func axisChannels(_ harkHex: String) -> (red: Double, green: Double, blue: Double)? {
    var hex = harkHex.trimmingCharacters(in: .whitespacesAndNewlines)
    if hex.hasPrefix("#") { hex.removeFirst() }
    guard hex.count == 6, let value = UInt32(hex, radix: 16) else { return nil }
    return axisChannels(value)
}

nonisolated private func axisChannels(_ value: UInt32) -> (red: Double, green: Double, blue: Double) {
    (
        red: Double((value >> 16) & 0xFF) / 255,
        green: Double((value >> 8) & 0xFF) / 255,
        blue: Double(value & 0xFF) / 255
    )
}

/// WCAG 2.1 relative luminance.
nonisolated private func axisLuminance(_ channels: (red: Double, green: Double, blue: Double)) -> Double {
    func linear(_ c: Double) -> Double {
        c <= 0.03928 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4)
    }
    return 0.2126 * linear(channels.red) + 0.7152 * linear(channels.green) + 0.0722 * linear(channels.blue)
}

nonisolated private func axisContrast(_ one: Double, _ other: Double) -> Double {
    (max(one, other) + 0.05) / (min(one, other) + 0.05)
}

nonisolated private let axisPaperDarkLuminance = axisLuminance(axisChannels(axisVoidRGB))
nonisolated private let axisPaperLightLuminance = axisLuminance(axisChannels(axisMistRGB))
nonisolated private let axisCarbonLuminance = axisLuminance(axisChannels(axisCarbonRGB))

/// The channels a server accent resolves to on one ground: the hex when it
/// clears the accent floor, the brand's own accent otherwise.
nonisolated private func axisAccentChannels(_ hex: String, dark: Bool) -> (red: Double, green: Double, blue: Double) {
    if let channels = axisChannels(hex) {
        let paper = dark ? axisPaperDarkLuminance : axisPaperLightLuminance
        if axisContrast(axisLuminance(channels), paper) >= axisAccentFloor {
            return channels
        }
    }
    return axisChannels(dark ? axisAccentRGB : axisAccentInkRGB)
}

nonisolated private extension UIColor {
    convenience init(axisRGB value: UInt32) {
        let channels = axisChannels(value)
        self.init(red: channels.red, green: channels.green, blue: channels.blue, alpha: 1)
    }
}

nonisolated extension Color {
    init(axisRGB value: UInt32) {
        let channels = axisChannels(value)
        self.init(red: channels.red, green: channels.green, blue: channels.blue)
    }

    init(axisDark dark: UInt32, light: UInt32) {
        self.init(uiColor: UIColor { traits in
            UIColor(axisRGB: traits.userInterfaceStyle == .dark ? dark : light)
        })
    }

    /// Parses a `#RRGGBB` string, the form the server's `accent_color` uses.
    /// Returns nil for anything else; callers fall back to `Axis.accent`.
    init?(harkHex: String) {
        guard let channels = axisChannels(harkHex) else { return nil }
        self.init(red: channels.red, green: channels.green, blue: channels.blue)
    }

    /// Returns a dynamic accent with a 3:1 contrast fallback.
    static func harkAccent(_ hex: String) -> Color {
        Color(uiColor: UIColor { traits in
            let channels = axisAccentChannels(hex, dark: traits.userInterfaceStyle == .dark)
            return UIColor(red: channels.red, green: channels.green, blue: channels.blue, alpha: 1)
        })
    }

    /// The label ink on a field filled with `harkAccent`: carbon where it
    /// clears AA, chalk where the accent is too deep for it.
    static func harkAccentInk(_ hex: String) -> Color {
        Color(uiColor: UIColor { traits in
            let channels = axisAccentChannels(hex, dark: traits.userInterfaceStyle == .dark)
            let carbon = axisContrast(axisLuminance(channels), axisCarbonLuminance) >= axisLabelFloor
            return UIColor(axisRGB: carbon ? axisCarbonRGB : axisChalkRGB)
        })
    }
}

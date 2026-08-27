//
//  AxisPalette.swift
//  Hark
//
//  The design tokens every Hark surface draws from: the app, the notification
//  service extension, and the widget extension. Each token resolves per
//  appearance. In dark they are the dashboard's tokens
//  (internal/dashboard/assets/app.css) spelled in Swift — pitch surfaces,
//  chalk ink, lacquer red as the single signal colour, jade for success. In
//  light the same instrument reads on industrial paper-white. Red never
//  means delivered.
//

import SwiftUI
import UIKit

nonisolated enum Axis {
    // MARK: Surfaces

    static let paper = Color(axisDark: axisPaperDarkRGB, light: axisPaperLightRGB)
    static let surface = Color(axisDark: 0x0B0E14, light: 0xFFFFFF)
    static let surface2 = Color(axisDark: 0x11161F, light: 0xEDEFF3)
    static let surface3 = Color(axisDark: 0x1A212C, light: 0xE2E5EB)
    static let field = Color(axisDark: 0x090C11, light: 0xECEEF2)

    // MARK: Ink

    static let ink = Color(axisDark: 0xF4F6F9, light: 0x10141B)
    static let inkMuted = Color(axisDark: 0xD5DBE4, light: 0x2A313C)
    static let inkSubtle = Color(axisDark: 0xA2ABB9, light: 0x525C6B)
    static let inkFaint = Color(axisDark: 0x8F99AB, light: 0x616B7B)
    /// Decorative only — indexes and ledger numbers hidden from assistive
    /// technology. It does not meet AA against the paper.
    static let inkDisabled = Color(axisDark: 0x4D5665, light: 0xB6BDC9)

    // MARK: Rules

    static let line = ink.opacity(0.12)
    static let lineStrong = ink.opacity(0.24)
    static let lineFaint = ink.opacity(0.06)

    // MARK: Signal

    /// Fills, strips, rules. Not small text.
    static let signal = Color(axisRGB: 0xCE2020)
    static let signalPressed = Color(axisRGB: 0xA31818)
    /// The signal colour at text weight; passes AA on every surface.
    static let signalText = Color(axisDark: axisAccentDarkRGB, light: axisAccentLightRGB)
    static let onSignal = Color.white
    static let signalWash = signal.opacity(0.12)
    static let signalLine = signal.opacity(0.55)

    // MARK: States

    static let ok = Color(axisDark: 0x16B37D, light: 0x0C7A54)
    static let okLine = ok.opacity(0.5)
    static let warn = Color(axisDark: 0xE2A81E, light: 0x8F6600)
    static let warnLine = warn.opacity(0.5)
    static let danger = signalText

    /// What a Live Activity's accent falls back to when the server's
    /// `accent_color` does not parse, or sits too close to the paper it is
    /// drawn on.
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
        static let fast: Double = 0.15
        static let base: Double = 0.22
        static var ease: Animation { .easeOut(duration: base) }
        static var quick: Animation { .easeOut(duration: fast) }
    }
}

nonisolated private let axisPaperDarkRGB: UInt32 = 0x06080D
nonisolated private let axisPaperLightRGB: UInt32 = 0xF4F5F8

nonisolated private let axisAccentDarkRGB: UInt32 = 0xE64949
nonisolated private let axisAccentLightRGB: UInt32 = 0xB91C1C

/// The floor an accent has to clear against the paper it is drawn on: the
/// 3:1 of WCAG's non-text contrast, since the accent carries glyphs, bars,
/// and keylines rather than body copy.
nonisolated private let axisAccentFloor: Double = 3

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

nonisolated private let axisPaperDarkLuminance = axisLuminance(axisChannels(axisPaperDarkRGB))
nonisolated private let axisPaperLightLuminance = axisLuminance(axisChannels(axisPaperLightRGB))

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

    /// A token resolved against the trait collection it draws under.
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

    /// The accent color a content state names, or lacquer red when the string
    /// does not parse. A Live Activity draws its accent as text, tints, bars,
    /// and keylines on the paper surfaces, so in each appearance an accent
    /// that cannot hold 3:1 against that appearance's paper falls back the
    /// same way an unparsable one does.
    static func harkAccent(_ hex: String) -> Color {
        guard let channels = axisChannels(hex) else { return Axis.accent }
        let luminance = axisLuminance(channels)
        return Color(uiColor: UIColor { traits in
            let dark = traits.userInterfaceStyle == .dark
            let paper = dark ? axisPaperDarkLuminance : axisPaperLightLuminance
            guard axisContrast(luminance, paper) >= axisAccentFloor else {
                return UIColor(axisRGB: dark ? axisAccentDarkRGB : axisAccentLightRGB)
            }
            return UIColor(red: channels.red, green: channels.green, blue: channels.blue, alpha: 1)
        })
    }
}

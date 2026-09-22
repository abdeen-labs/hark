//
//  AxisPalette.swift
//  Hark
//

import SwiftUI
import UIKit

nonisolated enum Axis {
    // MARK: Grounds

    static let paper = Color(axisDark: axisPaperDarkRGB, light: axisPaperLightRGB)
    static let surface = Color(axisDark: 0x0B0E14, light: 0xFFFFFF)
    static let surface2 = Color(axisDark: 0x11161F, light: 0xEDEFF3)
    static let surface3 = Color(axisDark: 0x1A212C, light: 0xE2E5EB)
    static let field = Color(axisDark: 0x090C11, light: 0xECEEF2)

    // MARK: Ink

    static let ink = Color(axisDark: axisInkDarkRGB, light: axisInkLightRGB)
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

    /// Lacquer red: fills, strips, rules, and lights. Not small text.
    static let signal = Color(axisRGB: axisSignalRGB)
    /// The filled lacquer on either ground.
    static let signalDeep = Color(axisRGB: axisSignalRGB)
    /// The signal colour at text weight; passes AA on the paper.
    static let signalText = Color(axisDark: axisAccentDarkRGB, light: axisAccentLightRGB)
    /// Dark ink on a field filled with `warn`, on either ground.
    static let onField = Color(axisRGB: axisInkLightRGB)
    static let signalWash = signal.opacity(0.12)
    static let signalLine = signal.opacity(0.55)

    // MARK: States

    /// Cobalt, the Abdeen Labs step for verified. Red is never success.
    static let ok = Color(axisDark: 0x5AA7FF, light: 0x1D5A96)
    static let okLine = ok.opacity(0.5)
    /// Neon yellow, the Abdeen Labs step for a warning. It sets a warning's
    /// line and label on the dark ground and fills a chip under `onField` on
    /// either one. It measures about 1:1 on the light paper, so it never sets
    /// light-ground ink.
    static let warn = Color(axisRGB: 0xF5FF00)
    /// The alarm step is Abdeen Labs scarlet, apart from the lacquer signal:
    /// a fault, a destructive control, a Critical alert. Here it is a dashed
    /// frame, a struck rule, a pulse, or a status light.
    static let alarm = Color(axisDark: axisScarletRGB, light: axisScarletDeepRGB)
    /// The alarm label; passes AA on the paper.
    static let alarmText = Color(axisDark: axisScarletRGB, light: axisScarletInkRGB)
    /// The alarm chip and the destructive control: the deeper scarlet on
    /// either ground, under `onAlarmField` or under the white a system
    /// control draws itself.
    static let alarmField = Color(axisRGB: axisScarletDeepRGB)
    /// Chalk ink on a filled scarlet field.
    static let onAlarmField = Color(axisRGB: 0xF3F7FF)

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

nonisolated private let axisPaperDarkRGB: UInt32 = 0x06080D
nonisolated private let axisPaperLightRGB: UInt32 = 0xF4F5F8
nonisolated private let axisInkDarkRGB: UInt32 = 0xF4F6F9
nonisolated private let axisInkLightRGB: UInt32 = 0x10141B

nonisolated private let axisSignalRGB: UInt32 = 0xCE2020
nonisolated private let axisScarletRGB: UInt32 = 0xFF002B
nonisolated private let axisScarletDeepRGB: UInt32 = 0xDB0023
nonisolated private let axisScarletInkRGB: UInt32 = 0xBD001D
nonisolated private let axisAccentDarkRGB: UInt32 = 0xE64949
nonisolated private let axisAccentLightRGB: UInt32 = 0xB91C1C

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

nonisolated private let axisPaperDarkLuminance = axisLuminance(axisChannels(axisPaperDarkRGB))
nonisolated private let axisPaperLightLuminance = axisLuminance(axisChannels(axisPaperLightRGB))
nonisolated private let axisInkLightLuminance = axisLuminance(axisChannels(axisInkLightRGB))

/// The channels a server accent resolves to on one ground: the hex when it
/// clears the accent floor, the brand's own accent otherwise.
nonisolated private func axisAccentChannels(_ hex: String, dark: Bool) -> (red: Double, green: Double, blue: Double) {
    if let channels = axisChannels(hex) {
        let paper = dark ? axisPaperDarkLuminance : axisPaperLightLuminance
        if axisContrast(axisLuminance(channels), paper) >= axisAccentFloor {
            return channels
        }
    }
    return axisChannels(dark ? axisAccentDarkRGB : axisAccentLightRGB)
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

    /// The label ink on a field filled with `harkAccent`: the dark ink where
    /// it clears AA, the light ink where the accent is too deep for it.
    static func harkAccentInk(_ hex: String) -> Color {
        Color(uiColor: UIColor { traits in
            let channels = axisAccentChannels(hex, dark: traits.userInterfaceStyle == .dark)
            let darkInk = axisContrast(axisLuminance(channels), axisInkLightLuminance) >= axisLabelFloor
            return UIColor(axisRGB: darkInk ? axisInkLightRGB : axisInkDarkRGB)
        })
    }
}

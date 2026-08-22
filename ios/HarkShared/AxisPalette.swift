//
//  AxisPalette.swift
//  Hark
//
//  The design tokens every Hark surface draws from: the app, the notification
//  service extension, and the widget extension. They are the dashboard's
//  tokens (internal/dashboard/assets/app.css) spelled in Swift — pitch
//  surfaces, chalk ink, lacquer red as the single signal colour, jade for
//  success. Red never means delivered.
//

import SwiftUI

nonisolated enum Axis {
    // MARK: Surfaces

    static let paper = Color(axisRGB: 0x06080D)
    static let surface = Color(axisRGB: 0x0B0E14)
    static let surface2 = Color(axisRGB: 0x11161F)
    static let surface3 = Color(axisRGB: 0x1A212C)
    static let field = Color(axisRGB: 0x090C11)

    // MARK: Ink

    static let ink = Color(axisRGB: 0xF4F6F9)
    static let inkMuted = Color(axisRGB: 0xD5DBE4)
    static let inkSubtle = Color(axisRGB: 0xA2ABB9)
    static let inkFaint = Color(axisRGB: 0x7B8496)
    /// Decorative only — indexes and ledger numbers hidden from assistive
    /// technology. It does not meet AA against the paper.
    static let inkDisabled = Color(axisRGB: 0x4D5665)

    // MARK: Rules

    static let line = ink.opacity(0.12)
    static let lineStrong = ink.opacity(0.24)
    static let lineFaint = ink.opacity(0.06)

    // MARK: Signal

    /// Fills, strips, rules. Not small text.
    static let signal = Color(axisRGB: 0xCE2020)
    static let signalPressed = Color(axisRGB: 0xA31818)
    /// The signal colour at text weight; passes AA on the paper.
    static let signalText = Color(axisRGB: 0xE13B3B)
    static let onSignal = Color.white
    static let signalWash = signal.opacity(0.12)
    static let signalLine = signal.opacity(0.55)

    // MARK: States

    static let ok = Color(axisRGB: 0x16B37D)
    static let okLine = ok.opacity(0.5)
    static let warn = Color(axisRGB: 0xE2A81E)
    static let warnLine = warn.opacity(0.5)
    static let danger = signalText

    /// What a Live Activity's accent falls back to when the server's
    /// `accent_color` does not parse.
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

nonisolated extension Color {
    init(axisRGB value: UInt32) {
        self.init(
            red: Double((value >> 16) & 0xFF) / 255,
            green: Double((value >> 8) & 0xFF) / 255,
            blue: Double(value & 0xFF) / 255
        )
    }

    /// Parses a `#RRGGBB` string, the form the server's `accent_color` uses.
    /// Returns nil for anything else; callers fall back to `Axis.accent`.
    init?(harkHex: String) {
        var hex = harkHex.trimmingCharacters(in: .whitespacesAndNewlines)
        if hex.hasPrefix("#") { hex.removeFirst() }
        guard hex.count == 6, let value = UInt32(hex, radix: 16) else { return nil }
        self.init(axisRGB: value)
    }

    /// The accent color a content state names, or lacquer red when the string
    /// does not parse.
    static func harkAccent(_ hex: String) -> Color {
        Color(harkHex: hex) ?? Axis.accent
    }
}

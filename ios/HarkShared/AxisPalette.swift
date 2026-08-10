//
//  AxisPalette.swift
//  Hark
//
//  The Axis design tokens: lacquer red on warm dark neutrals, jade for
//  success. Shared by the app, the notification service extension, and the
//  widget extension, so every surface draws from one palette.
//

import SwiftUI

/// The Axis palette. Dark-only, flat plates, restrained color.
nonisolated enum Axis {
    /// Lacquer red, the accent. #E13B3B.
    static let accent = Color(red: 225 / 255, green: 59 / 255, blue: 59 / 255)

    /// Jade, for success states. #3FA97B.
    static let jade = Color(red: 63 / 255, green: 169 / 255, blue: 123 / 255)

    /// Amber, for warnings and pending states. #D9A13B.
    static let amber = Color(red: 217 / 255, green: 161 / 255, blue: 59 / 255)

    /// The base background. Warm near-black. #161210.
    static let bg = Color(red: 0x16 / 255, green: 0x12 / 255, blue: 0x10 / 255)

    /// A flat plate sitting on the background. #1F1A17.
    static let plate = Color(red: 0x1F / 255, green: 0x1A / 255, blue: 0x17 / 255)

    /// A raised plate: one step lighter, still flat. #282221.
    static let plateRaised = Color(red: 0x28 / 255, green: 0x22 / 255, blue: 0x21 / 255)

    /// Hairline stroke around plates. #362E2A.
    static let stroke = Color(red: 0x36 / 255, green: 0x2E / 255, blue: 0x2A / 255)

    /// Primary text. Warm white. #F2EDE7.
    static let textPrimary = Color(red: 0xF2 / 255, green: 0xED / 255, blue: 0xE7 / 255)

    /// Secondary text. #A99E95.
    static let textSecondary = Color(red: 0xA9 / 255, green: 0x9E / 255, blue: 0x95 / 255)

    /// Tertiary text, for timestamps and footnotes. #7A716A.
    static let textTertiary = Color(red: 0x7A / 255, green: 0x71 / 255, blue: 0x6A / 255)
}

nonisolated extension Color {
    /// Parses a `#RRGGBB` string, the form the server's `accent_color` uses.
    /// Returns nil for anything else; callers fall back to `Axis.accent`.
    init?(harkHex: String) {
        var hex = harkHex.trimmingCharacters(in: .whitespacesAndNewlines)
        if hex.hasPrefix("#") { hex.removeFirst() }
        guard hex.count == 6, let value = UInt32(hex, radix: 16) else { return nil }
        self.init(
            red: Double((value >> 16) & 0xFF) / 255,
            green: Double((value >> 8) & 0xFF) / 255,
            blue: Double(value & 0xFF) / 255
        )
    }

    /// The accent color a content state names, or lacquer red when the string
    /// does not parse.
    static func harkAccent(_ hex: String) -> Color {
        Color(harkHex: hex) ?? Axis.accent
    }
}

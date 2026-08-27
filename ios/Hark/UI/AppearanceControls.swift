//
//  AppearanceControls.swift
//  Hark
//
//  The appearance setting: system, dark, or light. Stored in standard
//  defaults; the app root reads it and applies the colour scheme.
//

import SwiftUI

nonisolated enum Appearance: String, CaseIterable {
    case system, dark, light

    /// The standard-defaults key the setting is stored under.
    static let storageKey = "appearance"

    /// The scheme to force, or nil to follow the system.
    var colorScheme: ColorScheme? {
        switch self {
        case .system: nil
        case .dark: .dark
        case .light: .light
        }
    }

    var label: String {
        switch self {
        case .system: "System"
        case .dark: "Dark"
        case .light: "Light"
        }
    }
}

struct AppearanceModule: View {
    let index: String

    @AppStorage(Appearance.storageKey) private var appearance = Appearance.system.rawValue

    var body: some View {
        Module(index: index, label: "Appearance") {
            AxisChoiceChips(
                options: Appearance.allCases.map { (value: $0.rawValue, label: $0.label) },
                selection: Binding(
                    get: { appearance },
                    set: { appearance = $0 ?? Appearance.system.rawValue }
                )
            )
        }
    }
}

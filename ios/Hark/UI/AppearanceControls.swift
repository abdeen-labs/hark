//
//  AppearanceControls.swift
//  Hark
//

import SwiftUI

nonisolated enum Appearance: String, CaseIterable {
    case system, dark, light

    static let storageKey = "appearance"

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

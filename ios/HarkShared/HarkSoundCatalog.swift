//
//  HarkSoundCatalog.swift
//  Hark
//

import Foundation

nonisolated enum HarkSoundCatalog {
    static let appGroupID = "group.dev.abdeen.hark"
    static let selectionKey = "notification_sound"

    nonisolated struct Tone: Identifiable, Hashable, Sendable {
        let id: String
        let name: String

        var file: String { id + ".caf" }
    }

    static let tones: [Tone] = [
        Tone(id: "relay", name: "Relay"),
        Tone(id: "semaphore", name: "Semaphore"),
        Tone(id: "beacon", name: "Beacon"),
        Tone(id: "lattice", name: "Lattice"),
        Tone(id: "meridian", name: "Meridian"),
        Tone(id: "pulse", name: "Pulse"),
        Tone(id: "aperture", name: "Aperture"),
        Tone(id: "filament", name: "Filament"),
        Tone(id: "gantry", name: "Gantry"),
        Tone(id: "sonar", name: "Sonar"),
    ]

    static func tone(id: String) -> Tone? {
        tones.first { $0.id == id }
    }

    static var selectedToneID: String? {
        guard
            let defaults = UserDefaults(suiteName: appGroupID),
            let id = defaults.string(forKey: selectionKey),
            tone(id: id) != nil
        else { return nil }
        return id
    }

    static var selectedTone: Tone? {
        selectedToneID.flatMap(tone(id:))
    }

    static func select(_ tone: Tone?) {
        guard let defaults = UserDefaults(suiteName: appGroupID) else { return }
        if let tone {
            defaults.set(tone.id, forKey: selectionKey)
        } else {
            defaults.removeObject(forKey: selectionKey)
        }
    }
}

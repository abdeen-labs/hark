//
//  HarkSoundCatalog.swift
//  Hark
//
//  The bundled notification tones and the user's choice among them. The
//  choice lives in the app group so the notification service extension can
//  read it when it re-sounds a push; no selection, or an id no tone matches,
//  means the system default sound.
//

import Foundation

nonisolated enum HarkSoundCatalog {
    static let appGroupID = "group.dev.abdeen.hark"
    static let selectionKey = "notification_sound"

    nonisolated struct Tone: Identifiable, Hashable, Sendable {
        /// The filename stem; also the stored selection value.
        let id: String
        let name: String

        /// The bundled resource, `relay.caf`. Notification sounds resolve by
        /// filename against the app bundle, so the file carries no path.
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

    /// The selected tone's id, or nil for the system default. A missing
    /// app-group container reads as nil rather than failing.
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

    /// Stores the choice; nil restores the system default.
    static func select(_ tone: Tone?) {
        guard let defaults = UserDefaults(suiteName: appGroupID) else { return }
        if let tone {
            defaults.set(tone.id, forKey: selectionKey)
        } else {
            defaults.removeObject(forKey: selectionKey)
        }
    }
}

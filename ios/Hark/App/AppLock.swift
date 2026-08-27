//
//  AppLock.swift
//  Hark
//
//  The Face ID gate over the whole app. Locks when Hark leaves the
//  foreground; one device-owner authentication — Face ID, with the passcode
//  as its fallback — opens it. Turning the lock on or off takes the same
//  authentication, so a taken phone cannot switch it off.
//

import Foundation
import LocalAuthentication
import SwiftUI

@MainActor
@Observable
final class AppLock {
    static let enabledKey = "app_lock_enabled"

    private let defaults: UserDefaults

    private(set) var isEnabled: Bool
    private(set) var isLocked: Bool
    private(set) var isPrompting = false
    private(set) var isSceneActive = true

    /// One device-owner authentication. Tests inject their own verdict.
    var authorize: @MainActor () async -> Bool

    /// A declined prompt stays down until the next lock; the Unlock control
    /// is the way back in.
    private var promptDeclined = false

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        let enabled = defaults.bool(forKey: Self.enabledKey)
        isEnabled = enabled
        isLocked = enabled
        authorize = { await AppLock.evaluateDeviceOwner() }
    }

    /// The app-switcher cover: the lock is on and the scene is not front
    /// and active.
    var showsShield: Bool { isEnabled && !isSceneActive }

    /// Why device-owner authentication cannot run — no passcode, no enrolled
    /// biometry — or nil when it can. Read fresh each time; the answer moves
    /// with the phone's own settings.
    var unavailableReason: String? {
        let context = LAContext()
        var error: NSError?
        if context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) { return nil }
        return error?.localizedDescription ?? "Face ID and the device passcode are unavailable."
    }

    func handleScenePhase(_ phase: ScenePhase) {
        switch phase {
        case .active:
            isSceneActive = true
            if isLocked, !isPrompting, !promptDeclined {
                Task { [weak self] in
                    guard let self, isLocked, !isPrompting, !promptDeclined else { return }
                    await unlock()
                }
            }
        case .inactive:
            isSceneActive = false
        case .background:
            isSceneActive = false
            if isEnabled {
                isLocked = true
                promptDeclined = false
            }
        @unknown default:
            break
        }
    }

    func unlock() async {
        guard isLocked, !isPrompting else { return }
        isPrompting = true
        defer { isPrompting = false }
        if await authorize() {
            isLocked = false
            promptDeclined = false
        } else {
            promptDeclined = true
        }
    }

    /// Flips the setting only past one successful authentication — enabling
    /// puts the Face ID permission prompt right here in Settings, disabling
    /// keeps a thief out of the switch. Returns whether the change took.
    func setEnabled(_ enabled: Bool) async -> Bool {
        guard enabled != isEnabled else { return true }
        guard await authorize() else { return false }
        isEnabled = enabled
        defaults.set(enabled, forKey: Self.enabledKey)
        if !enabled { isLocked = false }
        return true
    }

    private static func evaluateDeviceOwner() async -> Bool {
        let context = LAContext()
        var error: NSError?
        guard context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) else { return false }
        do {
            return try await context.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: "Unlock Hark")
        } catch {
            return false
        }
    }
}

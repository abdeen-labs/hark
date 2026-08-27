//
//  AppLock.swift
//  Hark
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

    var authorize: @MainActor () async -> Bool

    private var promptDeclined = false

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        let enabled = defaults.bool(forKey: Self.enabledKey)
        isEnabled = enabled
        isLocked = enabled
        authorize = { await AppLock.evaluateDeviceOwner() }
    }

    var showsShield: Bool { isEnabled && !isSceneActive }

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

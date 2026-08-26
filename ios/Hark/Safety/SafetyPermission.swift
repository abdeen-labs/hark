//
//  SafetyPermission.swift
//  Hark
//
//  Critical Alert permission and safety-source display helpers.
//

import Foundation
import UserNotifications

nonisolated enum SafetyCriticalSupport {
    /// True only in builds signed with the
    /// com.apple.developer.usernotifications.critical-alerts entitlement.
    static let entitlementGranted = false
}

nonisolated enum CriticalAlertState {
    case unknown
    case notificationsDenied
    case notRequested
    case unavailable
    case granted
    case criticalDenied

    /// `requestedBefore` distinguishes an unrequested permission from a build
    /// where Critical Alerts are unavailable.
    static func classify(
        authorizationStatus: UNAuthorizationStatus,
        criticalSetting: UNNotificationSetting,
        requestedBefore: Bool,
        entitlementGranted: Bool
    ) -> CriticalAlertState {
        switch authorizationStatus {
        case .denied:
            return .notificationsDenied
        case .notDetermined:
            return .notRequested
        case .authorized, .provisional, .ephemeral:
            break
        @unknown default:
            return .unknown
        }
        switch criticalSetting {
        case .enabled:
            return .granted
        case .disabled:
            return .criticalDenied
        case .notSupported:
            if !requestedBefore { return .notRequested }
            return entitlementGranted ? .notRequested : .unavailable
        @unknown default:
            return .unknown
        }
    }
}

nonisolated enum SafetyKindDisplay {
    static let all = ["smoke", "carbon_monoxide", "panic", "intrusion", "water_leak"]

    static func allowsCritical(_ kind: String) -> Bool {
        all.contains(kind)
    }

    static func label(_ kind: String) -> String {
        switch kind {
        case "general": "General"
        case "smoke": "Smoke"
        case "carbon_monoxide": "Carbon monoxide"
        case "panic": "Panic"
        case "intrusion": "Intrusion"
        case "water_leak": "Water leak"
        default: kind.replacingOccurrences(of: "_", with: " ")
        }
    }
}

nonisolated enum SafetyTestFeedback {
    static func message(for error: HarkClientError) -> String {
        if error.isRateLimited {
            return "A test was sent recently. Try again in a few minutes."
        }
        return error.errorDescription ?? "The test could not be sent."
    }
}

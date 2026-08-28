//
//  SafetyPermission.swift
//  Hark
//
//  Critical Alert permission and setup-test display helpers.
//

import Foundation
import UserNotifications

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
        requestedBefore: Bool
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
            return requestedBefore ? .unavailable : .notRequested
        @unknown default:
            return .unknown
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

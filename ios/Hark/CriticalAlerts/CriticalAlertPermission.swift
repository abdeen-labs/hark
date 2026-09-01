//
//  CriticalAlertPermission.swift
//  Hark
//
//  Critical Alert permission state.
//

import Foundation
import UserNotifications

nonisolated enum CriticalAlertState {
    case unknown
    case notificationsDenied
    case notRequested
    case granted
    case criticalDenied

    static func classify(
        authorizationStatus: UNAuthorizationStatus,
        criticalSetting: UNNotificationSetting
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
            return .notRequested
        @unknown default:
            return .unknown
        }
    }
}

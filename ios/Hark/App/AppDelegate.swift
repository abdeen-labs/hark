//
//  AppDelegate.swift
//  Hark
//
//  UIKit's half of the push plumbing: the APNs token callbacks, the
//  notification categories, and the notification-center delegate that turns
//  action buttons into answers.
//

import UIKit
import UserNotifications

final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        let center = UNUserNotificationCenter.current()
        center.delegate = NotificationDelegate.shared
        center.setNotificationCategories(Self.categories)
        application.registerForRemoteNotifications()
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        AppModel.shared.handleAPNsToken(AppModel.hex(deviceToken))
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        // Registration reruns on the next foreground; nothing to do here.
    }

    /// The three categories the server's question pushes name. Versioned
    /// identifiers: changing a category's actions would relabel notifications
    /// already sitting on a Lock Screen.
    static var categories: Set<UNNotificationCategory> {
        let approval = UNNotificationCategory(
            identifier: HarkNotification.categoryApproval,
            actions: [
                UNNotificationAction(identifier: HarkNotification.actionApprove, title: "Approve", options: []),
                UNNotificationAction(identifier: HarkNotification.actionDeny, title: "Deny", options: [.destructive]),
            ],
            intentIdentifiers: [],
            options: []
        )
        let yesNo = UNNotificationCategory(
            identifier: HarkNotification.categoryYesNo,
            actions: [
                UNNotificationAction(identifier: HarkNotification.actionYes, title: "Yes", options: []),
                UNNotificationAction(identifier: HarkNotification.actionNo, title: "No", options: [.destructive]),
            ],
            intentIdentifiers: [],
            options: []
        )
        let reply = UNNotificationCategory(
            identifier: HarkNotification.categoryReply,
            actions: [
                UNTextInputNotificationAction(
                    identifier: HarkNotification.actionReply,
                    title: "Reply",
                    options: [],
                    textInputButtonTitle: "Send",
                    textInputPlaceholder: "Your reply"
                ),
            ],
            intentIdentifiers: [],
            options: []
        )
        return [approval, yesNo, reply]
    }
}

/// The notification-center delegate. Its methods are nonisolated — the
/// framework calls them from its own queue — and everything Sendable is
/// extracted before hopping to the main actor.
final class NotificationDelegate: NSObject, UNUserNotificationCenterDelegate {
    static let shared = NotificationDelegate()

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .list, .sound]
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let payload = HarkPushPayload.from(userInfo: response.notification.request.content.userInfo)
        let actionIdentifier = response.actionIdentifier
        let userText = (response as? UNTextInputNotificationResponse)?.userText

        await AppModel.shared.handleNotificationResponse(
            payload: payload,
            actionIdentifier: actionIdentifier,
            userText: userText
        )
    }
}

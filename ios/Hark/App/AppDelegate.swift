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

/// The notification-center delegate. The completion-handler variants are
/// implemented deliberately: the async forms let the compiler-generated thunk
/// invoke UIKit's completion on a background executor, and the didReceive
/// completion drives UIKit's snapshot/state-restoration pass, which asserts
/// off the main thread. Everything Sendable is extracted before hopping to
/// the main actor, and the handler is always called from there.
final class NotificationDelegate: NSObject, UNUserNotificationCenterDelegate {
    static let shared = NotificationDelegate()

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .list, .sound])
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let payload = HarkPushPayload.from(userInfo: response.notification.request.content.userInfo)
        let actionIdentifier = response.actionIdentifier
        let userText = (response as? UNTextInputNotificationResponse)?.userText

        Task { @MainActor in
            await AppModel.shared.handleNotificationResponse(
                payload: payload,
                actionIdentifier: actionIdentifier,
                userText: userText
            )
            completionHandler()
        }
    }
}

//
//  AppModel.swift
//  Hark
//
//  Shared application state for authentication, device registration,
//  Live Activity tokens, the inbox, and routing. UI-observed state runs on
//  MainActor.
//

import ActivityKit
import Foundation
import SwiftUI
import UserNotifications
import UIKit

@MainActor
@Observable
final class AppModel {
    static let shared = AppModel()

    enum Phase {
        case loading
        case signedOut
        case signedIn
    }

    enum Tab: Hashable {
        case inbox
        case history
        case devices
        case settings
    }

    static let defaultServerURL = URL(string: "https://hark.abdeen.dev")!

    // MARK: State

    private(set) var phase: Phase = .loading
    var selectedTab: Tab = .inbox

    private(set) var serverURL: URL
    private(set) var sessionToken: String?
    private(set) var user: APIUser?
    private(set) var sessionInfo: APISessionInfo?

    /// The server's row for this installation, stored after registration.
    private(set) var deviceID: String?
    private(set) var apnsTokenHex: String?
    private var latestPushToStartToken: String?

    private(set) var inbox: [InboxEntry] = []
    private(set) var inboxError: String?
    private(set) var inboxRefreshedAt: Date?

    private(set) var criticalAlertState: CriticalAlertState = .unknown
    private(set) var safetySettings: APISafetySettings?
    private(set) var safetySettingsError: String?

    private var activityTokenTasks: [String: Task<Void, Never>] = [:]
    private var pushToStartTask: Task<Void, Never>?
    private var activityWatchTask: Task<Void, Never>?

    var client: HarkClient { HarkClient(baseURL: serverURL, token: sessionToken) }

    /// sandbox for a debug build, production for a release build — the
    /// environment ActivityKit's tokens belong to travels with the token.
    nonisolated static var apnsEnvironment: String {
        #if DEBUG
        "sandbox"
        #else
        "production"
        #endif
    }

    private init() {
        let defaults = UserDefaults.standard
        if
            let stored = defaults.string(forKey: Self.serverURLKey),
            let url = URL(string: stored)
        {
            serverURL = url
        } else {
            serverURL = Self.defaultServerURL
        }
        deviceID = defaults.string(forKey: Self.deviceIDKey)
        sessionToken = Keychain.sessionToken
    }

    private static let serverURLKey = "server_url"
    private static let deviceIDKey = "device_id"
    private static let criticalAlertRequestedKey = "critical_alert_requested"

    // MARK: - Lifecycle

    /// Validates the stored session on launch. GET /v1/auth/session — the
    /// call also slides the session's expiry forward.
    func bootstrap() async {
        guard phase == .loading else { return }
        guard sessionToken != nil else {
            phase = .signedOut
            return
        }
        do {
            let session = try await client.session()
            user = session.user
            sessionInfo = session.session
            phase = .signedIn
            await afterSignIn()
        } catch let error as HarkClientError where error.isUnauthorized {
            clearCredentials()
            phase = .signedOut
        } catch {
            // Offline is not signed out: keep the session and carry on.
            phase = .signedIn
            await afterSignIn()
        }
    }

    /// Runs on every return to the foreground: re-validate, re-register,
    /// refresh, and reconcile — the client's own closing of the gap the
    /// transport never retries across.
    func didBecomeActive() async {
        guard phase == .signedIn else { return }
        UIApplication.shared.registerForRemoteNotifications()
        await refreshNotificationPermission()
        await registerDeviceIfPossible()
        await refreshInbox()
        await refreshSafetySettings()
        await reconcileLiveActivities()
        do {
            let session = try await client.session()
            user = session.user
            sessionInfo = session.session
        } catch let error as HarkClientError where error.isUnauthorized {
            handleUnauthorized()
        } catch {
            // Transient; nothing to do.
        }
    }

    // MARK: - Auth

    func signIn(serverText: String, username: String, password: String) async throws {
        guard let url = Self.normalizeServerURL(serverText) else {
            throw HarkClientError.badURL
        }
        let anonymous = HarkClient(baseURL: url, token: nil)
        let response = try await anonymous.login(
            username: username.trimmingCharacters(in: .whitespacesAndNewlines),
            password: password
        )
        serverURL = url
        UserDefaults.standard.set(url.absoluteString, forKey: Self.serverURLKey)
        sessionToken = response.token
        Keychain.sessionToken = response.token
        user = response.user
        sessionInfo = response.session
        phase = .signedIn
        selectedTab = .inbox
        await afterSignIn()
    }

    func signOut() async {
        let departing = client
        Task.detached { try? await departing.logout() }
        clearCredentials()
        stopLiveActivityObservers()
        inbox = []
        safetySettings = nil
        safetySettingsError = nil
        phase = .signedOut
        try? await UNUserNotificationCenter.current().setBadgeCount(0)
    }

    /// A 401 anywhere means the session is gone: back to sign-in.
    func handleUnauthorized() {
        guard phase == .signedIn else { return }
        clearCredentials()
        stopLiveActivityObservers()
        inbox = []
        safetySettings = nil
        safetySettingsError = nil
        phase = .signedOut
    }

    private func clearCredentials() {
        sessionToken = nil
        Keychain.sessionToken = nil
        user = nil
        sessionInfo = nil
        deviceID = nil
        UserDefaults.standard.removeObject(forKey: Self.deviceIDKey)
    }

    private func afterSignIn() async {
        await requestNotificationAuthorization()
        await refreshNotificationPermission()
        startLiveActivityObservers()
        await registerDeviceIfPossible()
        await refreshInbox()
        await refreshSafetySettings()
        await reconcileLiveActivities()
    }

    static func normalizeServerURL(_ text: String) -> URL? {
        var trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        if !trimmed.contains("://") {
            trimmed = "https://" + trimmed
        }
        while trimmed.hasSuffix("/") { trimmed.removeLast() }
        guard
            let url = URL(string: trimmed),
            let scheme = url.scheme?.lowercased(),
            scheme == "https" || scheme == "http",
            url.host != nil
        else { return nil }
        return url
    }

    // MARK: - Notifications and device registration

    private func requestNotificationAuthorization() async {
        let center = UNUserNotificationCenter.current()
        _ = try? await center.requestAuthorization(options: [.alert, .sound, .badge])
        UIApplication.shared.registerForRemoteNotifications()
    }

    func refreshNotificationPermission() async {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        criticalAlertState = CriticalAlertState.classify(
            authorizationStatus: settings.authorizationStatus,
            criticalSetting: settings.criticalAlertSetting,
            requestedBefore: UserDefaults.standard.bool(forKey: Self.criticalAlertRequestedKey),
            entitlementGranted: SafetyCriticalSupport.entitlementGranted
        )
    }

    func requestCriticalAlertAuthorization() async {
        let center = UNUserNotificationCenter.current()
        _ = try? await center.requestAuthorization(options: [.alert, .sound, .badge, .criticalAlert])
        UserDefaults.standard.set(true, forKey: Self.criticalAlertRequestedKey)
        UIApplication.shared.registerForRemoteNotifications()
        await refreshNotificationPermission()
    }

    // MARK: - Safety settings

    func refreshSafetySettings() async {
        guard phase == .signedIn else { return }
        do {
            safetySettings = try await client.safetySettings()
            safetySettingsError = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            handleUnauthorized()
        } catch {
            safetySettingsError = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    func setCriticalAlertsEnabled(_ enabled: Bool) async {
        guard phase == .signedIn else { return }
        let previous = safetySettings
        safetySettings = APISafetySettings(criticalAlertsEnabled: enabled)
        do {
            safetySettings = try await client.setSafetySettings(criticalAlertsEnabled: enabled)
            safetySettingsError = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            handleUnauthorized()
        } catch {
            safetySettings = previous
            safetySettingsError = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    /// Called by the app delegate whenever iOS hands over an APNs token.
    func handleAPNsToken(_ hex: String) {
        apnsTokenHex = hex
        Task { await registerDeviceIfPossible() }
    }

    /// POST /v1/devices — replace semantics, so this runs on every launch,
    /// every foreground, and every token change. The client sends its
    /// complete capability state each time; omission clears.
    func registerDeviceIfPossible() async {
        guard phase == .signedIn, let apnsTokenHex else { return }
        do {
            let device = try await client.registerDevice(
                RegisterDeviceRequest(
                    apnsToken: apnsTokenHex,
                    name: UIDevice.current.name,
                    interactionSchemaVersion: 1,
                    liveActivityInteractionVersion: 1
                )
            )
            deviceID = device.id
            UserDefaults.standard.set(device.id, forKey: Self.deviceIDKey)
            if let token = latestPushToStartToken {
                await sendPushToStartToken(token)
            }
        } catch let error as HarkClientError where error.isUnauthorized {
            handleUnauthorized()
        } catch {
            // Registration reruns on the next foreground.
        }
    }

    // MARK: - Live Activity token plumbing

    func startLiveActivityObservers() {
        if pushToStartTask == nil {
            pushToStartTask = Task { [weak self] in
                if let token = Activity<HarkActivityAttributes>.pushToStartToken {
                    await self?.handlePushToStartToken(Self.hex(token))
                }
                for await token in Activity<HarkActivityAttributes>.pushToStartTokenUpdates {
                    await self?.handlePushToStartToken(Self.hex(token))
                }
            }
        }
        if activityWatchTask == nil {
            activityWatchTask = Task { [weak self] in
                for activity in Activity<HarkActivityAttributes>.activities {
                    self?.watch(activity)
                }
                for await activity in Activity<HarkActivityAttributes>.activityUpdates {
                    self?.watch(activity)
                }
            }
        }
    }

    private func stopLiveActivityObservers() {
        pushToStartTask?.cancel()
        pushToStartTask = nil
        activityWatchTask?.cancel()
        activityWatchTask = nil
        for task in activityTokenTasks.values { task.cancel() }
        activityTokenTasks = [:]
    }

    private func handlePushToStartToken(_ hex: String) async {
        latestPushToStartToken = hex
        await sendPushToStartToken(hex)
    }

    /// PUT /v1/devices/{id}/push-to-start-token. Held until the device row
    /// exists; re-sent after every registration because registration is a
    /// replace.
    private func sendPushToStartToken(_ tokenHex: String) async {
        guard phase == .signedIn, let deviceID else { return }
        do {
            try await client.setPushToStartToken(
                deviceID: deviceID,
                token: tokenHex,
                environment: Self.apnsEnvironment
            )
        } catch let error as HarkClientError where error.isUnauthorized {
            handleUnauthorized()
        } catch {
            // The token is retained and re-sent on the next registration.
        }
    }

    /// Watches one activity for its per-activity update token. New activities
    /// arrive here from `activityUpdates` — including ones the server started
    /// by push while the app was in the background.
    private func watch(_ activity: Activity<HarkActivityAttributes>) {
        guard activityTokenTasks[activity.id] == nil else { return }
        activityTokenTasks[activity.id] = Task { [weak self] in
            if let current = activity.pushToken {
                await self?.reportActivityToken(Self.hex(current), for: activity)
            }
            for await token in activity.pushTokenUpdates {
                await self?.reportActivityToken(Self.hex(token), for: activity)
            }
        }
    }

    /// Reports a per-activity update token both ways the contract allows:
    /// the capability route the start push named (no session needed), and
    /// the session route, which turns the server's inference into a lookup.
    private func reportActivityToken(_ tokenHex: String, for activity: Activity<HarkActivityAttributes>) async {
        let attributes = activity.attributes
        let nativeID = activity.id
        let harkActivityID = activity.content.state.activityId

        if
            !attributes.tokenRegistrationToken.isEmpty,
            let endpoint = URL(string: attributes.tokenRegistrationUrl)
        {
            try? await HarkResponder.putUpdateToken(
                endpoint: endpoint,
                registrationToken: attributes.tokenRegistrationToken,
                nativeActivityID: nativeID,
                updateToken: tokenHex
            )
        }

        if phase == .signedIn, let deviceID {
            _ = try? await client.setActivityUpdateToken(
                deviceID: deviceID,
                updateToken: tokenHex,
                nativeActivityID: nativeID,
                activityID: harkActivityID.isEmpty ? nil : harkActivityID,
                environment: Self.apnsEnvironment
            )
        }
    }

    /// Ends any local Live Activity the server no longer lists — the client's
    /// half of "nothing is retried". Cards presenting a question are exempt:
    /// the server does not list them, and it resolves them itself.
    func reconcileLiveActivities() async {
        guard phase == .signedIn else { return }
        var liveIDs = Set<String>()
        var cursor: String?
        for _ in 0 ..< 5 {
            guard let page = try? await client.liveActivities(status: "live", cursor: cursor) else { return }
            for activity in page.activities { liveIDs.insert(activity.id) }
            guard let next = page.nextCursor else { break }
            cursor = next
        }
        for activity in Activity<HarkActivityAttributes>.activities {
            guard activity.attributes.question == nil else { continue }
            guard activity.content.state.interaction == nil else { continue }
            let id = activity.content.state.activityId
            if !id.isEmpty, !liveIDs.contains(id) {
                await activity.end(nil, dismissalPolicy: .immediate)
            }
        }
    }

    // MARK: - Inbox

    func refreshInbox() async {
        guard phase == .signedIn else { return }
        do {
            let pending = try await client.allInteractions(status: "pending")
            inbox = pending
            inboxError = nil
            inboxRefreshedAt = .now
            updateBadge()
        } catch let error as HarkClientError where error.isUnauthorized {
            handleUnauthorized()
        } catch {
            inboxError = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    /// The badge is the count of unanswered questions; the client knows it
    /// sooner than a push does, so the client keeps it.
    private func updateBadge() {
        let count = inbox.count
        Task { try? await UNUserNotificationCenter.current().setBadgeCount(count) }
    }

    /// Answers a question with the session. Inline surfaces call this.
    func answer(_ entry: InboxEntry, action: String, text: String? = nil) async throws -> APIInteraction {
        guard let deviceID else {
            throw HarkClientError.api(
                status: 0,
                code: "device_not_registered",
                message: "This device has not finished registering. Try again in a moment.",
                fields: []
            )
        }
        do {
            let updated = try await client.respond(
                id: entry.interaction.id,
                action: action,
                text: text,
                deviceID: deviceID,
                actionDigest: entry.interaction.actionDigest
            )
            inbox.removeAll { $0.id == updated.id }
            updateBadge()
            return updated
        } catch let error as HarkClientError where error.isUnauthorized {
            handleUnauthorized()
            throw error
        }
    }

    func cancel(_ entry: InboxEntry) async throws {
        do {
            _ = try await client.cancelInteraction(id: entry.interaction.id)
            inbox.removeAll { $0.id == entry.id }
            updateBadge()
        } catch let error as HarkClientError where error.isUnauthorized {
            handleUnauthorized()
            throw error
        }
    }

    // MARK: - Notification responses and taps

    /// Handles a notification action button or a body tap, forwarded by the
    /// app delegate. Runs even before bootstrap finishes: answering needs no
    /// session when the payload carries a response token.
    func handleNotificationResponse(
        payload: HarkPushPayload?,
        actionIdentifier: String,
        userText: String?
    ) async {
        guard let payload else { return }

        if let wireAction = HarkNotification.wireAction(for: actionIdentifier) {
            guard let question = payload.question else { return }
            let endpoint = serverURL.appendingPathComponent("v1/interactions/\(question.id)/response")
            let text = wireAction == "reply" ? userText : nil
            try? await HarkResponder.postAnswer(
                endpoint: endpoint,
                action: wireAction,
                text: text,
                deviceID: payload.deviceId,
                actionDigest: question.actionDigest,
                responseToken: question.responseToken,
                bearer: sessionToken
            )
            await refreshInbox()
            return
        }

        if actionIdentifier == UNNotificationDefaultActionIdentifier {
            if let urlString = payload.url, let url = HarkNotification.sanitizedTapURL(urlString) {
                await UIApplication.shared.open(url)
                return
            }
            selectedTab = payload.question != nil ? .inbox : .history
            if payload.question != nil { await refreshInbox() }
        }
    }

    // MARK: - Deep links

    /// hark:// opens the app; hark://inbox and hark://history route to tabs.
    func handleDeepLink(_ url: URL) {
        guard url.scheme?.lowercased() == "hark" else { return }
        let destination = (url.host ?? url.pathComponents.dropFirst().first ?? "").lowercased()
        switch destination {
        case "inbox":
            selectedTab = .inbox
        case "history":
            selectedTab = .history
        default:
            break
        }
    }

    /// Opens a tap destination from a notification, after re-checking it.
    func openTapURL(_ string: String) {
        guard let url = HarkNotification.sanitizedTapURL(string) else { return }
        UIApplication.shared.open(url)
    }

    // MARK: - Helpers

    nonisolated static func hex(_ data: Data) -> String {
        data.map { String(format: "%02x", $0) }.joined()
    }
}

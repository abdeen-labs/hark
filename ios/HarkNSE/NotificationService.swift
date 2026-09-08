//
//  NotificationService.swift
//  HarkNSE
//
//  Every Hark push arrives with mutable-content: 1 so this extension can
//  run. It downloads the sender's avatar and redraws the notification in
//  the communication style — an INSendMessageIntent donation with the
//  sender's name and image — so a service's notification reads like a
//  message from that service. When the push names a Wallet pass, it also
//  downloads the pass and stages it for the app. Anything that fails falls
//  back to the plain alert; a degraded notification always beats a dropped
//  one.
//

import Intents
import UserNotifications

final class NotificationService: UNNotificationServiceExtension {
    private var pendingHandler: ((UNNotificationContent) -> Void)?
    private var fallbackContent: UNNotificationContent?
    private var task: Task<Void, Never>?

    override func didReceive(
        _ request: UNNotificationRequest,
        withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void
    ) {
        pendingHandler = contentHandler
        let content = Self.applyingSelectedTone(request.content)
        fallbackContent = content

        guard let payload = HarkPushPayload.from(userInfo: request.content.userInfo) else {
            contentHandler(content)
            pendingHandler = nil
            return
        }

        task = Task {
            async let staged: Void = Self.stagePass(payload)
            let updated = await Self.communicationContent(for: content, payload: payload)
            await staged
            self.deliver(updated ?? content)
        }
    }

    override func serviceExtensionTimeWillExpire() {
        task?.cancel()
        if let fallbackContent {
            deliver(fallbackContent)
        }
    }

    private func deliver(_ content: UNNotificationContent) {
        guard let handler = pendingHandler else { return }
        pendingHandler = nil
        fallbackContent = nil
        handler(content)
    }

    // MARK: - Sound

    private static func applyingSelectedTone(_ content: UNNotificationContent) -> UNNotificationContent {
        guard
            content.interruptionLevel != .critical,
            let tone = HarkSoundCatalog.selectedTone,
            let mutable = content.mutableCopy() as? UNMutableNotificationContent
        else { return content }
        mutable.sound = UNNotificationSound(named: UNNotificationSoundName(tone.file))
        return mutable
    }

    // MARK: - Wallet pass

    /// Downloads the pass a push names and stages it for the app. Failure
    /// leaves nothing behind; the app fetches the pass again on tap.
    private static func stagePass(_ payload: HarkPushPayload) async {
        guard let url = payload.passURL else { return }
        guard let data = await HarkPassStore.download(from: url) else { return }
        try? HarkPassStore.write(data, recordID: payload.recordId)
    }

    // MARK: - Communication rendering

    /// Builds the communication-style content, or nil when it cannot.
    ///
    /// The sender's display name is the alert title, not the source name:
    /// `updating(from:)` promotes the sender to the notification's headline,
    /// and the headline belongs to the message ("iPad needs charging"), with
    /// the source carried by the avatar and the conversation instead.
    private static func communicationContent(
        for content: UNNotificationContent,
        payload: HarkPushPayload
    ) async -> UNNotificationContent? {
        let avatar = await downloadAvatar(payload.source.imageUrl)
        let headline = content.title.isEmpty ? payload.source.name : content.title

        let handle = INPersonHandle(value: payload.source.id, type: .unknown)
        let sender = INPerson(
            personHandle: handle,
            nameComponents: nil,
            displayName: headline,
            image: avatar,
            contactIdentifier: nil,
            customIdentifier: payload.source.id
        )

        let intent = INSendMessageIntent(
            recipients: nil,
            outgoingMessageType: .outgoingMessageText,
            content: content.body,
            speakableGroupName: INSpeakableString(spokenPhrase: headline),
            conversationIdentifier: payload.threadKey,
            serviceName: nil,
            sender: sender,
            attachments: nil
        )
        if let avatar {
            intent.setImage(avatar, forParameterNamed: \.sender)
        }

        let interaction = INInteraction(intent: intent, response: nil)
        interaction.direction = .incoming
        do {
            try await interaction.donate()
            return try content.updating(from: intent)
        } catch {
            return nil
        }
    }

    /// Downloads the sender's avatar from public HTTPS with a size limit.
    /// Failures return no avatar.
    private static func downloadAvatar(_ urlString: String?) async -> INImage? {
        guard
            let urlString,
            let url = URL(string: urlString),
            url.scheme?.lowercased() == "https"
        else { return nil }

        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 10
        config.timeoutIntervalForResource = 15
        let session = URLSession(configuration: config)

        guard let (data, response) = try? await session.data(from: url) else { return nil }
        guard
            let http = response as? HTTPURLResponse,
            (200 ..< 300).contains(http.statusCode),
            !data.isEmpty,
            data.count <= 5 * 1024 * 1024
        else { return nil }

        return INImage(imageData: data)
    }
}

//
//  HarkPushPayload.swift
//  Hark
//
//  The `hark` half of a notification payload, as docs/api.md documents it.
//  Decoded from the notification's userInfo by both the app (to handle taps
//  and action buttons) and the notification service extension (to draw the
//  communication-style notification).
//

import Foundation

nonisolated struct HarkPushPayload: Codable, Hashable, Sendable {
    nonisolated struct Source: Codable, Hashable, Sendable {
        var id: String
        var name: String
        var imageUrl: String?

        enum CodingKeys: String, CodingKey {
            case id
            case name
            case imageUrl = "image_url"
        }
    }

    nonisolated struct Question: Codable, Hashable, Sendable {
        var id: String
        var kind: String
        var category: String
        var actionDigest: String
        var responseToken: String?
        var primaryLabel: String?
        var secondaryLabel: String?
        /// RFC 3339 string. Kept raw; parse with `expiresDate`.
        var expiresAt: String

        enum CodingKeys: String, CodingKey {
            case id
            case kind
            case category
            case actionDigest = "action_digest"
            case responseToken = "response_token"
            case primaryLabel = "primary_label"
            case secondaryLabel = "secondary_label"
            case expiresAt = "expires_at"
        }

        var expiresDate: Date? { HarkDates.parse(expiresAt) }
    }

    var schemaVersion: Int
    var deviceId: String
    var recordId: String
    var threadKey: String
    var url: String?
    var source: Source
    var question: Question?

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case deviceId = "device_id"
        case recordId = "record_id"
        case threadKey = "thread_key"
        case url
        case source
        case question
    }

    /// Pulls the `hark` object out of a notification's userInfo. Returns nil
    /// for a notification that is not Hark's.
    static func from(userInfo: [AnyHashable: Any]) -> HarkPushPayload? {
        guard let hark = userInfo["hark"], JSONSerialization.isValidJSONObject(hark) else {
            return nil
        }
        guard let data = try? JSONSerialization.data(withJSONObject: hark) else {
            return nil
        }
        return try? JSONDecoder().decode(HarkPushPayload.self, from: data)
    }
}

/// The notification vocabulary the client registers: category identifiers and
/// the action identifiers behind their buttons. The last segment of an action
/// identifier is exactly the `action` string the answer endpoint takes.
nonisolated enum HarkNotification {
    static let categoryApproval = "hark.approval.v1"
    static let categoryYesNo = "hark.yes_no.v1"
    static let categoryReply = "hark.reply.v1"

    static let actionPrefix = "hark.action."
    static let actionApprove = "hark.action.approve"
    static let actionDeny = "hark.action.deny"
    static let actionYes = "hark.action.yes"
    static let actionNo = "hark.action.no"
    static let actionReply = "hark.action.reply"

    /// Strips `hark.action.` off an identifier, yielding the wire action.
    static func wireAction(for identifier: String) -> String? {
        guard identifier.hasPrefix(actionPrefix) else { return nil }
        return String(identifier.dropFirst(actionPrefix.count))
    }

    /// The schemes a tap destination must never use. The server checks too;
    /// a push has travelled a long way, so the client checks again.
    static let blockedURLSchemes: Set<String> = ["about", "blob", "data", "file", "javascript"]

    /// Re-checks a tap destination the way docs/api.md asks: length and scheme.
    static func sanitizedTapURL(_ string: String) -> URL? {
        guard string.count <= 2048, let url = URL(string: string) else { return nil }
        guard let scheme = url.scheme?.lowercased(), !blockedURLSchemes.contains(scheme) else {
            return nil
        }
        return url
    }
}

//
//  HarkAPIModels.swift
//  Hark
//
//  Wire types for the Hark HTTP API, written from docs/api.md. Every key is
//  spelled out in snake_case. Requests carry exactly the documented fields —
//  the server rejects unknown ones — and responses are decoded leniently,
//  ignoring fields this build does not recognize.
//

import Foundation

// MARK: - Auth

nonisolated struct APIUser: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var username: String
    var displayName: String
    var email: String?
    var createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case username
        case displayName = "display_name"
        case email
        case createdAt = "created_at"
    }
}

nonisolated struct APISessionInfo: Codable, Hashable, Sendable {
    var id: String
    var createdAt: Date
    var expiresAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case createdAt = "created_at"
        case expiresAt = "expires_at"
    }
}

nonisolated struct LoginRequest: Encodable, Sendable {
    var username: String
    var password: String
}

nonisolated struct LoginResponse: Decodable, Sendable {
    var token: String
    var expiresAt: Date
    var user: APIUser
    var session: APISessionInfo

    enum CodingKeys: String, CodingKey {
        case token
        case expiresAt = "expires_at"
        case user
        case session
    }
}

nonisolated struct SessionResponse: Decodable, Sendable {
    var kind: String
    var user: APIUser
    var session: APISessionInfo?

    enum CodingKeys: String, CodingKey {
        case kind
        case user
        case session
    }
}

// MARK: - Devices

nonisolated struct APIDevice: Decodable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String?
    var platform: String
    var active: Bool
    var interactionSchemaVersion: Int?
    var liveActivityInteractionVersion: Int?
    var liveActivityCapable: Bool
    var pushToStartEnvironment: String?
    var pushToStartUpdatedAt: Date?
    var createdAt: Date
    var lastSeenAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case name
        case platform
        case active
        case interactionSchemaVersion = "interaction_schema_version"
        case liveActivityInteractionVersion = "live_activity_interaction_version"
        case liveActivityCapable = "live_activity_capable"
        case pushToStartEnvironment = "push_to_start_environment"
        case pushToStartUpdatedAt = "push_to_start_updated_at"
        case createdAt = "created_at"
        case lastSeenAt = "last_seen_at"
    }
}

nonisolated struct DeviceResponse: Decodable, Sendable {
    var device: APIDevice
}

nonisolated struct DeviceListResponse: Decodable, Sendable {
    var devices: [APIDevice]
}

nonisolated struct RegisterDeviceRequest: Encodable, Sendable {
    var apnsToken: String
    var name: String?
    var interactionSchemaVersion: Int?
    var liveActivityInteractionVersion: Int?

    enum CodingKeys: String, CodingKey {
        case apnsToken = "apns_token"
        case name
        case interactionSchemaVersion = "interaction_schema_version"
        case liveActivityInteractionVersion = "live_activity_interaction_version"
    }
}

nonisolated struct PushToStartTokenRequest: Encodable, Sendable {
    var token: String
    var environment: String
    var schemaVersion: Int

    enum CodingKeys: String, CodingKey {
        case token
        case environment
        case schemaVersion = "schema_version"
    }
}

nonisolated struct ActivityUpdateTokenRequest: Encodable, Sendable {
    var updateToken: String
    var nativeActivityId: String?
    var activityId: String?
    var environment: String
    var schemaVersion: Int

    enum CodingKeys: String, CodingKey {
        case updateToken = "update_token"
        case nativeActivityId = "native_activity_id"
        case activityId = "activity_id"
        case environment
        case schemaVersion = "schema_version"
    }
}

nonisolated struct ActivityUpdateTokenResponse: Decodable, Sendable {
    var activityId: String
    var deliveryId: String

    enum CodingKeys: String, CodingKey {
        case activityId = "activity_id"
        case deliveryId = "delivery_id"
    }
}

// MARK: - Interactions

nonisolated struct APIInteraction: Decodable, Hashable, Identifiable, Sendable {
    var id: String
    var title: String
    var prompt: String
    var kind: String
    var presentation: String
    var status: String
    var choices: [String]
    var response: String?
    var url: String?
    var imageUrl: String?
    var actionDigest: String
    var primaryLabel: String?
    var secondaryLabel: String?
    var correlationId: String?
    var acceptedCount: Int?
    var respondingDeviceId: String?
    var expiresAt: Date
    var createdAt: Date
    var respondedAt: Date?
    var canceledAt: Date?

    enum CodingKeys: String, CodingKey {
        case id
        case title
        case prompt
        case kind
        case presentation
        case status
        case choices
        case response
        case url
        case imageUrl = "image_url"
        case actionDigest = "action_digest"
        case primaryLabel = "primary_label"
        case secondaryLabel = "secondary_label"
        case correlationId = "correlation_id"
        case acceptedCount = "accepted_count"
        case respondingDeviceId = "responding_device_id"
        case expiresAt = "expires_at"
        case createdAt = "created_at"
        case respondedAt = "responded_at"
        case canceledAt = "canceled_at"
    }

    var isPending: Bool { status == "pending" }

    /// The label/action pairs an answer surface renders, in order.
    var answerChoices: [(label: String, action: String)] {
        switch kind {
        case "approval":
            return [
                (primaryLabel ?? "Approve", "approve"),
                (secondaryLabel ?? "Deny", "deny"),
            ]
        case "yes_no":
            return [
                (primaryLabel ?? "Yes", "yes"),
                (secondaryLabel ?? "No", "no"),
            ]
        default:
            return []
        }
    }
}

/// A list item: an interaction plus the sender the list resolves for it.
nonisolated struct InboxEntry: Decodable, Hashable, Identifiable, Sendable {
    var interaction: APIInteraction
    var sourceName: String?
    var sourceImageUrl: String?

    var id: String { interaction.id }

    enum ExtraKeys: String, CodingKey {
        case sourceName = "source_name"
        case sourceImageUrl = "source_image_url"
    }

    init(from decoder: Decoder) throws {
        interaction = try APIInteraction(from: decoder)
        let c = try decoder.container(keyedBy: ExtraKeys.self)
        sourceName = try? c.decodeIfPresent(String.self, forKey: .sourceName)
        sourceImageUrl = try? c.decodeIfPresent(String.self, forKey: .sourceImageUrl)
    }

    init(interaction: APIInteraction, sourceName: String? = nil, sourceImageUrl: String? = nil) {
        self.interaction = interaction
        self.sourceName = sourceName
        self.sourceImageUrl = sourceImageUrl
    }
}

nonisolated struct InteractionsPage: Decodable, Sendable {
    var interactions: [InboxEntry]
    var nextCursor: String?

    enum CodingKeys: String, CodingKey {
        case interactions
        case nextCursor = "next_cursor"
    }
}

nonisolated struct InteractionEnvelope: Decodable, Sendable {
    var interaction: APIInteraction
}

nonisolated struct RespondRequest: Encodable, Sendable {
    var action: String
    var text: String?
    var deviceId: String
    var actionDigest: String
    var responseToken: String?

    enum CodingKeys: String, CodingKey {
        case action
        case text
        case deviceId = "device_id"
        case actionDigest = "action_digest"
        case responseToken = "response_token"
    }
}

// MARK: - History

nonisolated struct HistoryItem: Decodable, Hashable, Identifiable, Sendable {
    /// Composite `<source>:<row id>`; the handle a delete takes.
    var id: String
    var kind: String
    var sourceName: String?
    var sourceImageUrl: String?
    var title: String?
    var detail: String?
    var url: String?
    var result: String?
    var status: String?
    var deliveredCount: Int?
    var error: String?
    var priority: String?
    var createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case kind
        case sourceName = "source_name"
        case sourceImageUrl = "source_image_url"
        case title
        case detail
        case url
        case result
        case status
        case deliveredCount = "delivered_count"
        case error
        case priority
        case createdAt = "created_at"
    }
}

nonisolated struct HistoryPage: Decodable, Sendable {
    var items: [HistoryItem]
    var nextCursor: String?

    enum CodingKeys: String, CodingKey {
        case items
        case nextCursor = "next_cursor"
    }
}

nonisolated struct HistorySourcesResponse: Decodable, Sendable {
    var sources: [String]
}

// MARK: - Live Activities (read side, for reconciliation)

nonisolated struct APIActivity: Decodable, Hashable, Identifiable, Sendable {
    var id: String
    var status: String

    enum CodingKeys: String, CodingKey {
        case id
        case status
    }
}

nonisolated struct ActivitiesPage: Decodable, Sendable {
    var activities: [APIActivity]
    var nextCursor: String?

    enum CodingKeys: String, CodingKey {
        case activities
        case nextCursor = "next_cursor"
    }
}

// MARK: - Safety

nonisolated struct APISafetySource: Decodable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String
    var kind: String
    var criticalEnabled: Bool
    var createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case name
        case kind
        case criticalEnabled = "critical_enabled"
        case createdAt = "created_at"
    }
}

nonisolated struct SafetySourceResponse: Decodable, Sendable {
    var source: APISafetySource
}

nonisolated struct SafetySourceListResponse: Decodable, Sendable {
    var sources: [APISafetySource]
}

nonisolated struct CreateSafetySourceRequest: Encodable, Sendable {
    var name: String

    enum CodingKeys: String, CodingKey {
        case name
    }
}

nonisolated struct UpdateSafetySourceRequest: Encodable, Sendable {
    var kind: String?
    var name: String?
    var criticalEnabled: Bool?

    enum CodingKeys: String, CodingKey {
        case kind
        case name
        case criticalEnabled = "critical_enabled"
    }
}

nonisolated struct APISafetySettings: Codable, Hashable, Sendable {
    var criticalAlertsEnabled: Bool

    enum CodingKeys: String, CodingKey {
        case criticalAlertsEnabled = "critical_alerts_enabled"
    }
}

nonisolated struct APISafetyTestEvent: Decodable, Hashable, Sendable {
    var id: String
    var priority: String
    var status: String

    enum CodingKeys: String, CodingKey {
        case id
        case priority
        case status
    }
}

nonisolated struct SafetyTestResponse: Decodable, Sendable {
    var event: APISafetyTestEvent
}

// MARK: - Errors

nonisolated struct APIErrorField: Decodable, Hashable, Sendable {
    var field: String
    var message: String
}

nonisolated struct APIErrorEnvelope: Decodable, Sendable {
    nonisolated struct Payload: Decodable, Sendable {
        var code: String
        var message: String
        var fields: [APIErrorField]?
    }

    var error: Payload
}

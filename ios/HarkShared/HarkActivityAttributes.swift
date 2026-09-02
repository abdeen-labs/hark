//
//  HarkActivityAttributes.swift
//  Hark
//
//  The ActivityKit contract. The type is named exactly `HarkActivityAttributes`
//  because the server's start push names it in `attributes-type`; a different
//  name makes every start push land nowhere, silently.
//
//  ActivityKit decodes both the attributes and the content state with its own
//  JSON decoder, so every key is spelled out in snake_case here rather than
//  relying on a key strategy, and decoding is lenient: a key this build does
//  not recognize is ignored, and a missing optional becomes a default rather
//  than a failure. Dropping a push on the floor because a field was absent is
//  the one failure mode this file must never have.
//

import ActivityKit
import Foundation

nonisolated struct HarkActivityAttributes: ActivityAttributes, Hashable {
    /// The APNs content-state document, delivered verbatim from the server.
    nonisolated struct ContentState: Codable, Hashable {
        var schemaVersion: Int
        var activityId: String
        var title: String
        var status: String
        var detail: String?
        var progress: Double?
        /// RFC 3339 string; kept as a string so decoding never depends on a
        /// date strategy this file does not control.
        var updatedAt: String
        /// terminal | code | build | success | warning
        var symbol: String
        /// standard | private
        var privacyMode: String
        /// #RRGGBB
        var accentColor: String
        /// standard | ring | hero | terminal | steps | approval | shell | verdict | signal
        var style: String
        var interaction: InteractionState?

        enum CodingKeys: String, CodingKey {
            case schemaVersion = "schema_version"
            case activityId = "activity_id"
            case title
            case status
            case detail
            case progress
            case updatedAt = "updated_at"
            case symbol
            case privacyMode = "privacy_mode"
            case accentColor = "accent_color"
            case style
            case interaction
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            schemaVersion = (try? c.decodeIfPresent(Int.self, forKey: .schemaVersion)) ?? 1
            activityId = (try? c.decodeIfPresent(String.self, forKey: .activityId)) ?? ""
            title = (try? c.decodeIfPresent(String.self, forKey: .title)) ?? "Hark"
            status = (try? c.decodeIfPresent(String.self, forKey: .status)) ?? ""
            detail = try? c.decodeIfPresent(String.self, forKey: .detail)
            progress = try? c.decodeIfPresent(Double.self, forKey: .progress)
            updatedAt = (try? c.decodeIfPresent(String.self, forKey: .updatedAt)) ?? ""
            symbol = (try? c.decodeIfPresent(String.self, forKey: .symbol)) ?? "terminal"
            privacyMode = (try? c.decodeIfPresent(String.self, forKey: .privacyMode)) ?? "standard"
            accentColor = (try? c.decodeIfPresent(String.self, forKey: .accentColor)) ?? "#FE002A"
            style = (try? c.decodeIfPresent(String.self, forKey: .style)) ?? "standard"
            interaction = try? c.decodeIfPresent(InteractionState.self, forKey: .interaction)
        }

        init(
            schemaVersion: Int = 1,
            activityId: String,
            title: String,
            status: String,
            detail: String? = nil,
            progress: Double? = nil,
            updatedAt: String = "",
            symbol: String = "terminal",
            privacyMode: String = "standard",
            accentColor: String = "#FE002A",
            style: String = "standard",
            interaction: InteractionState? = nil
        ) {
            self.schemaVersion = schemaVersion
            self.activityId = activityId
            self.title = title
            self.status = status
            self.detail = detail
            self.progress = progress
            self.updatedAt = updatedAt
            self.symbol = symbol
            self.privacyMode = privacyMode
            self.accentColor = accentColor
            self.style = style
            self.interaction = interaction
        }
    }

    /// The question a card presents, when it presents one. Labels and actions
    /// travel together so a button never has to map one to the other.
    nonisolated struct InteractionState: Codable, Hashable {
        var id: String
        var kind: String
        var prompt: String
        var primaryLabel: String
        var secondaryLabel: String
        var primaryAction: String
        var secondaryAction: String
        /// pending, or the terminal interaction status once answered.
        var state: String

        enum CodingKeys: String, CodingKey {
            case id
            case kind
            case prompt
            case primaryLabel = "primary_label"
            case secondaryLabel = "secondary_label"
            case primaryAction = "primary_action"
            case secondaryAction = "secondary_action"
            case state
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = (try? c.decodeIfPresent(String.self, forKey: .id)) ?? ""
            kind = (try? c.decodeIfPresent(String.self, forKey: .kind)) ?? "approval"
            prompt = (try? c.decodeIfPresent(String.self, forKey: .prompt)) ?? ""
            primaryLabel = (try? c.decodeIfPresent(String.self, forKey: .primaryLabel)) ?? "Approve"
            secondaryLabel = (try? c.decodeIfPresent(String.self, forKey: .secondaryLabel)) ?? "Deny"
            primaryAction = (try? c.decodeIfPresent(String.self, forKey: .primaryAction)) ?? "approve"
            secondaryAction = (try? c.decodeIfPresent(String.self, forKey: .secondaryAction)) ?? "deny"
            state = (try? c.decodeIfPresent(String.self, forKey: .state)) ?? "pending"
        }

        init(
            id: String,
            kind: String = "approval",
            prompt: String,
            primaryLabel: String = "Approve",
            secondaryLabel: String = "Deny",
            primaryAction: String = "approve",
            secondaryAction: String = "deny",
            state: String = "pending"
        ) {
            self.id = id
            self.kind = kind
            self.prompt = prompt
            self.primaryLabel = primaryLabel
            self.secondaryLabel = secondaryLabel
            self.primaryAction = primaryAction
            self.secondaryAction = secondaryAction
            self.state = state
        }
    }

    /// The credentials a Lock Screen button answers with. Same three values a
    /// question notification carries.
    nonisolated struct Question: Codable, Hashable {
        var id: String
        var actionDigest: String
        var responseToken: String?

        enum CodingKeys: String, CodingKey {
            case id
            case actionDigest = "action_digest"
            case responseToken = "response_token"
        }
    }

    var schemaVersion: Int
    /// This card, on this phone.
    var deliveryId: String
    /// Needed to answer a question and to report a token.
    var deviceId: String
    /// Where to PUT the per-activity update token ActivityKit hands the app.
    var tokenRegistrationUrl: String
    /// The capability authorizing that PUT. Scoped to this delivery.
    var tokenRegistrationToken: String
    var question: Question?

    enum CodingKeys: String, CodingKey {
        case schemaVersion = "schema_version"
        case deliveryId = "delivery_id"
        case deviceId = "device_id"
        case tokenRegistrationUrl = "token_registration_url"
        case tokenRegistrationToken = "token_registration_token"
        case question
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = (try? c.decodeIfPresent(Int.self, forKey: .schemaVersion)) ?? 1
        deliveryId = (try? c.decodeIfPresent(String.self, forKey: .deliveryId)) ?? ""
        deviceId = (try? c.decodeIfPresent(String.self, forKey: .deviceId)) ?? ""
        tokenRegistrationUrl = (try? c.decodeIfPresent(String.self, forKey: .tokenRegistrationUrl)) ?? ""
        tokenRegistrationToken = (try? c.decodeIfPresent(String.self, forKey: .tokenRegistrationToken)) ?? ""
        question = try? c.decodeIfPresent(Question.self, forKey: .question)
    }

    init(
        schemaVersion: Int = 1,
        deliveryId: String,
        deviceId: String,
        tokenRegistrationUrl: String,
        tokenRegistrationToken: String,
        question: Question? = nil
    ) {
        self.schemaVersion = schemaVersion
        self.deliveryId = deliveryId
        self.deviceId = deviceId
        self.tokenRegistrationUrl = tokenRegistrationUrl
        self.tokenRegistrationToken = tokenRegistrationToken
        self.question = question
    }
}

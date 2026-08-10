//
//  AnswerActivityIntent.swift
//  HarkWidgets
//
//  The App Intent behind a Lock Screen button. It performs in the widget
//  extension's process, which holds no session — the response token from
//  the activity's attributes is the whole credential, so an answer is
//  device-bound and needs nothing from the app.
//

import AppIntents
import Foundation

struct AnswerActivityIntent: AppIntent {
    static let title: LocalizedStringResource = "Answer a Hark question"
    static let isDiscoverable = false

    @Parameter(title: "Endpoint")
    var endpoint: String

    @Parameter(title: "Action")
    var action: String

    @Parameter(title: "Device")
    var deviceID: String

    @Parameter(title: "Digest")
    var actionDigest: String

    @Parameter(title: "Token")
    var responseToken: String

    init() {}

    init(endpoint: String, action: String, deviceID: String, actionDigest: String, responseToken: String) {
        self.endpoint = endpoint
        self.action = action
        self.deviceID = deviceID
        self.actionDigest = actionDigest
        self.responseToken = responseToken
    }

    func perform() async throws -> some IntentResult {
        guard
            !action.isEmpty,
            !deviceID.isEmpty,
            let url = URL(string: endpoint)
        else { return .result() }

        // Success needs no local state change: the server resolves the card
        // itself, pushing the answered state and a dismissal date.
        try? await HarkResponder.postAnswer(
            endpoint: url,
            action: action,
            text: nil,
            deviceID: deviceID,
            actionDigest: actionDigest,
            responseToken: responseToken.isEmpty ? nil : responseToken
        )
        return .result()
    }
}

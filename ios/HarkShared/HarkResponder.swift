//
//  HarkResponder.swift
//  Hark
//
//  The sessionless network calls: answering a question with the push's
//  one-shot response_token, and reporting a Live Activity update token
//  through the capability route. Both are device-bound — no keychain, no
//  session — which is why the widget extension can use them.
//
//  Requests are encoded with exactly the documented fields; the server
//  rejects unknown ones.
//

import Foundation

nonisolated enum HarkResponderError: Error {
    case badURL
    case server(status: Int, code: String?)
    case invalidResponse
}

nonisolated enum HarkResponder {
    private static let session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 20
        config.timeoutIntervalForResource = 30
        return URLSession(configuration: config)
    }()

    // MARK: Answering a question

    private struct AnswerBody: Encodable {
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

    /// POSTs an answer to `/v1/interactions/{id}/response`.
    ///
    /// `responseToken` is the push's one-shot credential; `bearer` is the
    /// app's session when it has one. Either suffices on its own.
    static func postAnswer(
        endpoint: URL,
        action: String,
        text: String?,
        deviceID: String,
        actionDigest: String,
        responseToken: String?,
        bearer: String? = nil
    ) async throws {
        let body = AnswerBody(
            action: action,
            text: text,
            deviceId: deviceID,
            actionDigest: actionDigest,
            responseToken: responseToken
        )
        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let bearer {
            request.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization")
        }
        request.httpBody = try JSONEncoder().encode(body)
        try await run(request)
    }

    // MARK: Reporting an update token through the capability route

    private struct UpdateTokenBody: Encodable {
        var registrationToken: String
        var nativeActivityId: String?
        var updateToken: String

        enum CodingKeys: String, CodingKey {
            case registrationToken = "registration_token"
            case nativeActivityId = "native_activity_id"
            case updateToken = "update_token"
        }
    }

    /// PUTs the per-activity update token to the `token_registration_url`
    /// the start push carried. No auth header: the capability in the body is
    /// the credential.
    static func putUpdateToken(
        endpoint: URL,
        registrationToken: String,
        nativeActivityID: String?,
        updateToken: String
    ) async throws {
        let body = UpdateTokenBody(
            registrationToken: registrationToken,
            nativeActivityId: nativeActivityID,
            updateToken: updateToken
        )
        var request = URLRequest(url: endpoint)
        request.httpMethod = "PUT"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONEncoder().encode(body)
        try await run(request)
    }

    // MARK: URL derivation

    /// The origin (scheme + host + port) of a URL, with no path or query.
    static func origin(of url: URL) -> URL? {
        guard var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            return nil
        }
        components.path = ""
        components.query = nil
        components.fragment = nil
        return components.url
    }

    /// Derives the answer endpoint from the attributes' registration URL —
    /// the widget's only knowledge of where the server lives.
    static func answerEndpoint(tokenRegistrationUrl: String, interactionID: String) -> URL? {
        guard
            let registration = URL(string: tokenRegistrationUrl),
            let origin = origin(of: registration)
        else { return nil }
        return URL(string: origin.absoluteString + "/v1/interactions/\(interactionID)/response")
    }

    // MARK: Transport

    private static func run(_ request: URLRequest) async throws {
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw HarkResponderError.invalidResponse
        }
        guard (200 ..< 300).contains(http.statusCode) else {
            throw HarkResponderError.server(status: http.statusCode, code: errorCode(in: data))
        }
    }

    private struct ErrorEnvelope: Decodable {
        struct Payload: Decodable { var code: String }
        var error: Payload
    }

    private static func errorCode(in data: Data) -> String? {
        (try? JSONDecoder().decode(ErrorEnvelope.self, from: data))?.error.code
    }
}

//
//  HarkClient.swift
//  Hark
//
//  The session-authenticated HTTP client. A value: base URL plus token,
//  cheap to recreate whenever either changes. Every call speaks the /v1
//  surface exactly as docs/api.md documents it — snake_case JSON, bearer
//  auth, the one error envelope.
//

import Foundation

nonisolated enum HarkClientError: Error, LocalizedError {
    /// The server answered with its error envelope.
    case api(status: Int, code: String, message: String, fields: [APIErrorField])
    /// The response was not something this client recognizes.
    case invalidResponse
    /// The URL could not be built.
    case badURL
    /// The network failed underneath the request.
    case transport(Error)

    var isUnauthorized: Bool {
        if case .api(let status, _, _, _) = self { return status == 401 }
        return false
    }

    var isRateLimited: Bool {
        if case .api(let status, _, _, _) = self { return status == 429 }
        return false
    }

    var errorDescription: String? {
        switch self {
        case .api(_, _, let message, let fields):
            if let field = fields.first {
                return "\(message) (\(field.field): \(field.message))"
            }
            return message
        case .invalidResponse:
            return "The server's response was not understood."
        case .badURL:
            return "That server address does not look right."
        case .transport(let error):
            return (error as NSError).localizedDescription
        }
    }
}

nonisolated struct HarkClient: Sendable {
    var baseURL: URL
    var token: String?

    private static let session: URLSession = {
        let config = URLSessionConfiguration.default
        // Long-polls hold a request open for up to 25 s; leave headroom.
        config.timeoutIntervalForRequest = 40
        config.timeoutIntervalForResource = 60
        config.httpAdditionalHeaders = ["Accept": "application/json"]
        return URLSession(configuration: config)
    }()

    static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = HarkDates.decoderStrategy()
        return decoder
    }()

    static let encoder = JSONEncoder()

    // MARK: - Auth

    func login(username: String, password: String) async throws -> LoginResponse {
        try await send(
            "POST", "/v1/auth/login",
            body: LoginRequest(username: username, password: password),
            authenticated: false
        )
    }

    func logout() async throws {
        try await sendExpectingNoContent("POST", "/v1/auth/logout")
    }

    func session() async throws -> SessionResponse {
        try await send("GET", "/v1/auth/session")
    }

    // MARK: - Devices

    func registerDevice(_ request: RegisterDeviceRequest) async throws -> APIDevice {
        let response: DeviceResponse = try await send("POST", "/v1/devices", body: request)
        return response.device
    }

    func devices() async throws -> [APIDevice] {
        let response: DeviceListResponse = try await send("GET", "/v1/devices")
        return response.devices
    }

    func deleteDevice(id: String) async throws {
        try await sendExpectingNoContent("DELETE", "/v1/devices/\(id)")
    }

    func setPushToStartToken(deviceID: String, token: String, environment: String) async throws {
        try await sendExpectingNoContent(
            "PUT", "/v1/devices/\(deviceID)/push-to-start-token",
            body: PushToStartTokenRequest(token: token, environment: environment, schemaVersion: 1)
        )
    }

    func setActivityUpdateToken(
        deviceID: String,
        updateToken: String,
        nativeActivityID: String?,
        activityID: String?,
        environment: String
    ) async throws -> ActivityUpdateTokenResponse {
        try await send(
            "PUT", "/v1/devices/\(deviceID)/activity-update-token",
            body: ActivityUpdateTokenRequest(
                updateToken: updateToken,
                nativeActivityId: nativeActivityID,
                activityId: activityID,
                environment: environment,
                schemaVersion: 1
            )
        )
    }

    // MARK: - Interactions

    func interactions(status: String = "pending", cursor: String? = nil) async throws -> InteractionsPage {
        var query = [URLQueryItem(name: "status", value: status)]
        if let cursor { query.append(URLQueryItem(name: "cursor", value: cursor)) }
        return try await send("GET", "/v1/interactions", query: query)
    }

    /// Fetches the complete interactions snapshot for one status, following
    /// `next_cursor` until the server reports no more pages.
    func allInteractions(status: String = "pending") async throws -> [InboxEntry] {
        try await Self.collectInteractions { cursor in
            try await self.interactions(status: status, cursor: cursor)
        }
    }

    /// Drains a keyset-paginated interactions listing: a nil cursor first,
    /// then every `next_cursor` the server hands back until it returns none.
    /// Order is first-seen, entries are deduplicated by ID across page
    /// boundaries, and a cursor the server repeats fails closed rather than
    /// looping forever. Any page's error propagates; no partial array is
    /// ever returned.
    static func collectInteractions(
        fetchPage: (String?) async throws -> InteractionsPage
    ) async throws -> [InboxEntry] {
        var entries: [InboxEntry] = []
        var seenIDs = Set<String>()
        var seenCursors = Set<String>()
        var cursor: String?
        while true {
            let page = try await fetchPage(cursor)
            for entry in page.interactions where seenIDs.insert(entry.id).inserted {
                entries.append(entry)
            }
            guard let next = page.nextCursor else { return entries }
            guard seenCursors.insert(next).inserted else {
                throw HarkClientError.invalidResponse
            }
            cursor = next
        }
    }

    func interaction(id: String, waitSeconds: Int = 0) async throws -> APIInteraction {
        var query: [URLQueryItem] = []
        if waitSeconds > 0 {
            query.append(URLQueryItem(name: "wait_seconds", value: String(waitSeconds)))
        }
        let envelope: InteractionEnvelope = try await send("GET", "/v1/interactions/\(id)", query: query)
        return envelope.interaction
    }

    func respond(
        id: String,
        action: String,
        text: String? = nil,
        deviceID: String,
        actionDigest: String,
        responseToken: String? = nil
    ) async throws -> APIInteraction {
        let envelope: InteractionEnvelope = try await send(
            "POST", "/v1/interactions/\(id)/response",
            body: RespondRequest(
                action: action,
                text: text,
                deviceId: deviceID,
                actionDigest: actionDigest,
                responseToken: responseToken
            )
        )
        return envelope.interaction
    }

    func cancelInteraction(id: String) async throws -> APIInteraction {
        let envelope: InteractionEnvelope = try await send("POST", "/v1/interactions/\(id)/cancel")
        return envelope.interaction
    }

    // MARK: - History

    func history(kind: String = "all", cursor: String? = nil) async throws -> HistoryPage {
        var query = [URLQueryItem(name: "kind", value: kind)]
        if let cursor { query.append(URLQueryItem(name: "cursor", value: cursor)) }
        return try await send("GET", "/v1/history", query: query)
    }

    func deleteHistoryItem(id: String) async throws {
        try await sendExpectingNoContent("DELETE", "/v1/history/\(id)")
    }

    // MARK: - Live Activities (read side)

    func liveActivities(status: String = "live", cursor: String? = nil) async throws -> ActivitiesPage {
        var query = [URLQueryItem(name: "status", value: status)]
        if let cursor { query.append(URLQueryItem(name: "cursor", value: cursor)) }
        return try await send("GET", "/v1/activities", query: query)
    }

    // MARK: - Safety

    func safetySources() async throws -> [APISafetySource] {
        let response: SafetySourceListResponse = try await send("GET", "/v1/safety-sources")
        return response.sources
    }

    func createSafetySource(name: String) async throws -> APISafetySource {
        let response: SafetySourceResponse = try await send(
            "POST", "/v1/safety-sources",
            body: CreateSafetySourceRequest(name: name)
        )
        return response.source
    }

    func updateSafetySource(
        id: String,
        kind: String? = nil,
        criticalEnabled: Bool? = nil
    ) async throws -> APISafetySource {
        let response: SafetySourceResponse = try await send(
            "PATCH", "/v1/safety-sources/\(id)",
            body: UpdateSafetySourceRequest(kind: kind, name: nil, criticalEnabled: criticalEnabled)
        )
        return response.source
    }

    func deleteSafetySource(id: String) async throws {
        try await sendExpectingNoContent("DELETE", "/v1/safety-sources/\(id)")
    }

    func sendSafetyTest(sourceId: String) async throws -> APISafetyTestEvent {
        let response: SafetyTestResponse = try await send("POST", "/v1/safety-sources/\(sourceId)/test")
        return response.event
    }

    func safetySettings() async throws -> APISafetySettings {
        try await send("GET", "/v1/safety-settings")
    }

    func setSafetySettings(criticalAlertsEnabled: Bool) async throws -> APISafetySettings {
        try await send(
            "PATCH", "/v1/safety-settings",
            body: APISafetySettings(criticalAlertsEnabled: criticalAlertsEnabled)
        )
    }

    // MARK: - Transport

    private func makeURL(_ path: String, query: [URLQueryItem]) throws -> URL {
        guard var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false) else {
            throw HarkClientError.badURL
        }
        var basePath = components.path
        if basePath.hasSuffix("/") { basePath.removeLast() }
        components.path = basePath + path
        if !query.isEmpty { components.queryItems = query }
        guard let url = components.url else { throw HarkClientError.badURL }
        return url
    }

    private func makeRequest(
        _ method: String,
        _ path: String,
        query: [URLQueryItem],
        bodyData: Data?,
        authenticated: Bool
    ) throws -> URLRequest {
        var request = URLRequest(url: try makeURL(path, query: query))
        request.httpMethod = method
        if authenticated, let token {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let bodyData {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = bodyData
        }
        return request
    }

    private func perform(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await Self.session.data(for: request)
        } catch {
            throw HarkClientError.transport(error)
        }
        guard let http = response as? HTTPURLResponse else {
            throw HarkClientError.invalidResponse
        }
        guard (200 ..< 300).contains(http.statusCode) else {
            if let envelope = try? Self.decoder.decode(APIErrorEnvelope.self, from: data) {
                throw HarkClientError.api(
                    status: http.statusCode,
                    code: envelope.error.code,
                    message: envelope.error.message,
                    fields: envelope.error.fields ?? []
                )
            }
            throw HarkClientError.api(
                status: http.statusCode,
                code: "http_\(http.statusCode)",
                message: "The server answered \(http.statusCode).",
                fields: []
            )
        }
        return (data, http)
    }

    /// Sends a request with no body and decodes the response.
    private func send<R: Decodable>(
        _ method: String,
        _ path: String,
        query: [URLQueryItem] = [],
        authenticated: Bool = true
    ) async throws -> R {
        let request = try makeRequest(method, path, query: query, bodyData: nil, authenticated: authenticated)
        let (data, _) = try await perform(request)
        do {
            return try Self.decoder.decode(R.self, from: data)
        } catch {
            throw HarkClientError.invalidResponse
        }
    }

    /// Sends a request with a body and decodes the response.
    private func send<B: Encodable, R: Decodable>(
        _ method: String,
        _ path: String,
        query: [URLQueryItem] = [],
        body: B,
        authenticated: Bool = true
    ) async throws -> R {
        let bodyData = try Self.encoder.encode(body)
        let request = try makeRequest(method, path, query: query, bodyData: bodyData, authenticated: authenticated)
        let (data, _) = try await perform(request)
        do {
            return try Self.decoder.decode(R.self, from: data)
        } catch {
            throw HarkClientError.invalidResponse
        }
    }

    /// Sends a request whose success is 204 No Content.
    private func sendExpectingNoContent(
        _ method: String,
        _ path: String,
        authenticated: Bool = true
    ) async throws {
        let request = try makeRequest(method, path, query: [], bodyData: nil, authenticated: authenticated)
        _ = try await perform(request)
    }

    /// Sends a request with a body whose success is 204 No Content.
    private func sendExpectingNoContent<B: Encodable>(
        _ method: String,
        _ path: String,
        body: B,
        authenticated: Bool = true
    ) async throws {
        let bodyData = try Self.encoder.encode(body)
        let request = try makeRequest(method, path, query: [], bodyData: bodyData, authenticated: authenticated)
        _ = try await perform(request)
    }
}

//
//  HarkPassStore.swift
//  Hark
//
//  A pass staged for Wallet. The notification service extension downloads
//  the `.pkpass` a push names and leaves it in the app group container,
//  keyed by the push's `pass_record_id` — the same row id that follows the
//  source prefix in a history item's composite id — so the app can offer it
//  to Wallet from the tap or from history without going back to the
//  network. The file is a cache: a miss means fetching `pass_url` again.
//

import Foundation

nonisolated enum HarkPassStore {
    static let appGroupID = "group.dev.abdeen.hark"
    static let maxBytes = 10 * 1024 * 1024
    static let contentType = "application/vnd.apple.pkpass"

    private static let session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 10
        config.timeoutIntervalForResource = 20
        return URLSession(configuration: config)
    }()

    // MARK: Keys

    /// The row id behind a history item's `<source>:<row id>`: the value a
    /// push carries as `pass_record_id`.
    static func recordID(historyID: String) -> String {
        guard let colon = historyID.firstIndex(of: ":") else { return historyID }
        return String(historyID[historyID.index(after: colon)...])
    }

    private static let keyCharacters = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-_"))

    /// A record id is a UUID or a synthetic id from the same alphabet;
    /// anything else is not a file name.
    private static func fileName(recordID: String) -> String? {
        guard
            !recordID.isEmpty,
            recordID.count <= 128,
            recordID.unicodeScalars.allSatisfy(keyCharacters.contains)
        else { return nil }
        return recordID + ".pkpass"
    }

    // MARK: Files

    static var directory: URL? {
        FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: appGroupID)?
            .appending(path: "Passes", directoryHint: .isDirectory)
    }

    static func fileURL(recordID: String) -> URL? {
        guard let directory, let name = fileName(recordID: recordID) else { return nil }
        return directory.appending(path: name, directoryHint: .notDirectory)
    }

    static func write(_ data: Data, recordID: String) throws {
        guard let directory, let url = fileURL(recordID: recordID) else {
            throw CocoaError(.fileWriteUnknown)
        }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try data.write(to: url, options: .atomic)
    }

    static func read(recordID: String) -> Data? {
        guard let url = fileURL(recordID: recordID) else { return nil }
        return try? Data(contentsOf: url)
    }

    static func delete(recordID: String) {
        guard let url = fileURL(recordID: recordID) else { return }
        try? FileManager.default.removeItem(at: url)
    }

    static func deleteAll() {
        guard let directory else { return }
        try? FileManager.default.removeItem(at: directory)
    }

    // MARK: Download

    /// Fetches a pass from public HTTPS: a 2xx answer, a pkpass content
    /// type, and at most `maxBytes`. Anything else yields nothing.
    static func download(from url: URL) async -> Data? {
        guard url.scheme?.lowercased() == "https" else { return nil }
        guard let (data, response) = try? await session.data(from: url) else { return nil }
        guard
            let http = response as? HTTPURLResponse,
            (200 ..< 300).contains(http.statusCode),
            let type = http.value(forHTTPHeaderField: "Content-Type")?.lowercased(),
            type.hasPrefix(contentType),
            !data.isEmpty,
            data.count <= maxBytes
        else { return nil }
        return data
    }
}

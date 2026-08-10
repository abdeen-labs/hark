//
//  HarkDates.swift
//  Hark
//
//  Every timestamp the server sends is RFC 3339 UTC with millisecond
//  precision and a literal Z. These helpers parse that form (tolerating a
//  timestamp without the fractional part) and render it back.
//
//  Formatters are created per call rather than cached: ISO8601DateFormatter
//  is not Sendable, and these run from several isolation domains.
//

import Foundation

nonisolated enum HarkDates {
    /// Parses an RFC 3339 timestamp, with or without fractional seconds.
    static func parse(_ string: String) -> Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: string) { return date }
        let whole = ISO8601DateFormatter()
        whole.formatOptions = [.withInternetDateTime]
        return whole.date(from: string)
    }

    /// Renders a Date the way the API spells timestamps.
    static func format(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.string(from: date)
    }

    /// A JSONDecoder date strategy for the API's timestamps.
    static func decoderStrategy() -> JSONDecoder.DateDecodingStrategy {
        .custom { decoder in
            let container = try decoder.singleValueContainer()
            let raw = try container.decode(String.self)
            guard let date = parse(raw) else {
                throw DecodingError.dataCorruptedError(
                    in: container,
                    debugDescription: "Unrecognized timestamp: \(raw)"
                )
            }
            return date
        }
    }
}

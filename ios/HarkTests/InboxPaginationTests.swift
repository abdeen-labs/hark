//
//  InboxPaginationTests.swift
//  HarkTests
//
//  Characterizes HarkClient.collectInteractions: the collector must drain a
//  keyset-paginated listing completely, preserve first-seen order, deduplicate
//  by ID across page boundaries, fail closed on a repeated cursor, and never
//  return partial results when a page errors.
//

import XCTest
@testable import Hark

final class InboxPaginationTests: XCTestCase {

    // MARK: - Fixtures

    /// A scripted stand-in for the network: hands out pages (or errors) in
    /// order and records every cursor the collector requested.
    private final class PageScript {
        private(set) var requestedCursors: [String?] = []
        private var results: [Result<InteractionsPage, Error>]

        init(_ results: [Result<InteractionsPage, Error>]) {
            self.results = results
        }

        func fetch(_ cursor: String?) throws -> InteractionsPage {
            requestedCursors.append(cursor)
            guard !results.isEmpty else {
                XCTFail("Collector fetched more pages than scripted")
                throw HarkClientError.invalidResponse
            }
            return try results.removeFirst().get()
        }
    }

    private func entry(_ id: String) -> InboxEntry {
        InboxEntry(
            interaction: APIInteraction(
                id: id,
                title: "Question \(id)",
                prompt: "Prompt \(id)",
                kind: "yes_no",
                presentation: "notification",
                status: "pending",
                choices: [],
                response: nil,
                url: nil,
                imageUrl: nil,
                actionDigest: "digest-\(id)",
                primaryLabel: nil,
                secondaryLabel: nil,
                correlationId: nil,
                acceptedCount: nil,
                respondingDeviceId: nil,
                expiresAt: Date(timeIntervalSince1970: 2_000_000_000),
                createdAt: Date(timeIntervalSince1970: 1_000_000_000),
                respondedAt: nil,
                canceledAt: nil
            )
        )
    }

    private func page(_ ids: [String], next: String? = nil) -> InteractionsPage {
        InteractionsPage(interactions: ids.map(entry), nextCursor: next)
    }

    // MARK: - Cases

    func testSinglePageStopsAtNilCursor() async throws {
        let script = PageScript([
            .success(page(["a", "b"])),
        ])
        let entries = try await HarkClient.collectInteractions { try script.fetch($0) }
        XCTAssertEqual(entries.map(\.id), ["a", "b"])
        XCTAssertEqual(script.requestedCursors, [nil])
    }

    func testThreePagesPreserveOrder() async throws {
        let script = PageScript([
            .success(page(["a", "b"], next: "cursor1")),
            .success(page(["c", "d"], next: "cursor2")),
            .success(page(["e"])),
        ])
        let entries = try await HarkClient.collectInteractions { try script.fetch($0) }
        XCTAssertEqual(entries.map(\.id), ["a", "b", "c", "d", "e"])
        XCTAssertEqual(script.requestedCursors, [nil, "cursor1", "cursor2"])
    }

    func testDuplicateIDAcrossBoundaryAppearsOnce() async throws {
        let script = PageScript([
            .success(page(["a", "b"], next: "cursor1")),
            .success(page(["b", "c"])),
        ])
        let entries = try await HarkClient.collectInteractions { try script.fetch($0) }
        XCTAssertEqual(entries.map(\.id), ["a", "b", "c"])
        XCTAssertEqual(script.requestedCursors, [nil, "cursor1"])
    }

    func testRepeatedCursorFailsClosed() async {
        let script = PageScript([
            .success(page(["a"], next: "loop")),
            .success(page(["b"], next: "loop")),
        ])
        do {
            _ = try await HarkClient.collectInteractions { try script.fetch($0) }
            XCTFail("A repeated cursor must throw instead of looping")
        } catch HarkClientError.invalidResponse {
            // Expected: the cycle is detected on the second page's cursor.
        } catch {
            XCTFail("Expected HarkClientError.invalidResponse, got \(error)")
        }
        XCTAssertEqual(script.requestedCursors, [nil, "loop"])
    }

    func testErrorOnLaterPagePropagatesWithoutPartialResults() async {
        let script = PageScript([
            .success(page(["a"], next: "cursor1")),
            .failure(HarkClientError.api(status: 500, code: "internal", message: "boom", fields: [])),
        ])
        do {
            _ = try await HarkClient.collectInteractions { try script.fetch($0) }
            XCTFail("A failing page must propagate its error, not return partial data")
        } catch HarkClientError.api(let status, _, _, _) {
            // Throwing is what prevents partial data: no array is returned.
            XCTAssertEqual(status, 500)
        } catch {
            XCTFail("Expected HarkClientError.api, got \(error)")
        }
        XCTAssertEqual(script.requestedCursors, [nil, "cursor1"])
    }

    func testEmptyPagesWithNextCursorAreFollowed() async throws {
        let script = PageScript([
            .success(page([], next: "cursor1")),
            .success(page(["a"], next: "cursor2")),
            .success(page([])),
        ])
        let entries = try await HarkClient.collectInteractions { try script.fetch($0) }
        XCTAssertEqual(entries.map(\.id), ["a"])
        XCTAssertEqual(script.requestedCursors, [nil, "cursor1", "cursor2"])
    }
}

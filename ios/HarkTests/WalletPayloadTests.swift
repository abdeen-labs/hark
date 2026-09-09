import Foundation
import XCTest
@testable import Hark

final class WalletPayloadTests: XCTestCase {
    func testQuestionPassUsesTheHistoryCacheKey() throws {
        let payload = try JSONDecoder().decode(HarkPushPayload.self, from: Data("""
        {"schema_version":1,"device_id":"phone","record_id":"question-id", "thread_key":"interaction-question-id",
         "pass_url":"https://tickets.example.com/pass.pkpass","pass_record_id":"event-id",
         "source":{"id":"service","name":"Tickets"},
         "question":{"id":"question-id","kind":"yes_no","category":"hark.yes_no.v1","action_digest":"digest","expires_at":"2026-09-10T00:00:00Z"}}
        """.utf8))
        XCTAssertEqual(payload.recordId, "question-id")
        XCTAssertEqual(payload.question?.id, "question-id")
        XCTAssertEqual(payload.passRecordID, HarkPassStore.recordID(historyID: "event:event-id"))
        XCTAssertNotEqual(payload.passRecordID, payload.recordId)
    }

}

//
//  SafetyPermissionTests.swift
//  HarkTests
//
//  Safety helper tests.
//

import UserNotifications
import XCTest
@testable import Hark

final class SafetyPermissionTests: XCTestCase {

    // MARK: - CriticalAlertState.classify

    private func classify(
        _ status: UNAuthorizationStatus,
        _ critical: UNNotificationSetting,
        requestedBefore: Bool = false
    ) -> CriticalAlertState {
        CriticalAlertState.classify(
            authorizationStatus: status,
            criticalSetting: critical,
            requestedBefore: requestedBefore
        )
    }

    func testDeniedNotificationsDominate() {
        XCTAssertEqual(classify(.denied, .notSupported), .notificationsDenied)
        XCTAssertEqual(
            classify(.denied, .enabled, requestedBefore: true),
            .notificationsDenied
        )
        XCTAssertEqual(classify(.denied, .disabled, requestedBefore: true), .notificationsDenied)
    }

    func testEnabledSettingIsGranted() {
        XCTAssertEqual(classify(.authorized, .enabled, requestedBefore: true), .granted)
        XCTAssertEqual(classify(.provisional, .enabled, requestedBefore: true), .granted)
    }

    func testDisabledSettingIsCriticalDenied() {
        XCTAssertEqual(classify(.authorized, .disabled, requestedBefore: true), .criticalDenied)
        XCTAssertEqual(classify(.authorized, .disabled, requestedBefore: false), .criticalDenied)
    }

    func testNeverRequestedIsNotRequested() {
        XCTAssertEqual(classify(.authorized, .notSupported), .notRequested)
        XCTAssertEqual(classify(.notDetermined, .notSupported), .notRequested)
    }

    func testRequestedButUnsupportedIsUnavailable() {
        XCTAssertEqual(classify(.authorized, .notSupported, requestedBefore: true), .unavailable)
        XCTAssertEqual(classify(.provisional, .notSupported, requestedBefore: true), .unavailable)
    }

    // MARK: - SafetyTestFeedback

    func testRateLimitedTestGetsFriendlyCopy() {
        let error = HarkClientError.api(status: 429, code: "rate_limited", message: "Too many requests.", fields: [])
        XCTAssertTrue(error.isRateLimited)
        let message = SafetyTestFeedback.message(for: error)
        XCTAssertFalse(message.isEmpty)
        XCTAssertNotEqual(message, error.errorDescription)
    }

    func testOtherErrorsSurfaceTheirOwnDescription() {
        let error = HarkClientError.api(status: 422, code: "validation_failed", message: "Name is required.", fields: [])
        XCTAssertFalse(error.isRateLimited)
        XCTAssertEqual(SafetyTestFeedback.message(for: error), error.errorDescription)
    }

    func testNonAPIErrorsAreNotRateLimited() {
        XCTAssertFalse(HarkClientError.invalidResponse.isRateLimited)
        XCTAssertFalse(HarkClientError.badURL.isRateLimited)
    }

    // MARK: - AxisState priorities

    func testPriorityTones() {
        guard case .danger = AxisState.tone("critical") else {
            return XCTFail("critical must read as danger")
        }
        guard case .warn = AxisState.tone("time_sensitive") else {
            return XCTFail("time_sensitive must read as warn")
        }
        guard case .kind = AxisState.tone("normal") else {
            return XCTFail("normal must stay a plain kind tag")
        }
    }

    // MARK: - Safety source request encoding

    func testCreateSourceIncludesCriticalSwitch() throws {
        let data = try JSONEncoder().encode(CreateSafetySourceRequest(
            name: "Home Assistant",
            imageUrl: nil,
            url: nil,
            criticalEnabled: true
        ))
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(object.keys.sorted(), ["critical_enabled", "name"])
        XCTAssertEqual(object["name"] as? String, "Home Assistant")
        XCTAssertEqual(object["critical_enabled"] as? Bool, true)
    }

    func testUpdateCarriesTheSameDefaultsAsAService() throws {
        let data = try JSONEncoder().encode(UpdateSafetySourceRequest(
            name: "Front door",
            imageUrl: "https://example.com/front-door.png",
            url: "hark-test://front-door",
            criticalEnabled: true
        ))
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(object.keys.sorted(), ["critical_enabled", "image_url", "name", "url"])
        XCTAssertEqual(object["image_url"] as? String, "https://example.com/front-door.png")
        XCTAssertEqual(object["url"] as? String, "hark-test://front-door")
    }

    func testUpdateCanClearOptionalDefaults() throws {
        let data = try JSONEncoder().encode(UpdateSafetySourceRequest(
            name: "Front door",
            imageUrl: nil,
            url: nil,
            criticalEnabled: false
        ))
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertTrue(object["image_url"] is NSNull)
        XCTAssertTrue(object["url"] is NSNull)
        XCTAssertEqual(object["critical_enabled"] as? Bool, false)
    }
}

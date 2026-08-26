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
        requestedBefore: Bool = false,
        entitlementGranted: Bool = false
    ) -> CriticalAlertState {
        CriticalAlertState.classify(
            authorizationStatus: status,
            criticalSetting: critical,
            requestedBefore: requestedBefore,
            entitlementGranted: entitlementGranted
        )
    }

    func testDeniedNotificationsDominate() {
        XCTAssertEqual(classify(.denied, .notSupported), .notificationsDenied)
        XCTAssertEqual(
            classify(.denied, .enabled, requestedBefore: true, entitlementGranted: true),
            .notificationsDenied
        )
        XCTAssertEqual(classify(.denied, .disabled, requestedBefore: true), .notificationsDenied)
    }

    func testEnabledSettingIsGranted() {
        XCTAssertEqual(classify(.authorized, .enabled, requestedBefore: true, entitlementGranted: true), .granted)
        XCTAssertEqual(classify(.provisional, .enabled, requestedBefore: true, entitlementGranted: true), .granted)
    }

    func testDisabledSettingIsCriticalDenied() {
        XCTAssertEqual(classify(.authorized, .disabled, requestedBefore: true, entitlementGranted: true), .criticalDenied)
        XCTAssertEqual(classify(.authorized, .disabled, requestedBefore: false), .criticalDenied)
    }

    func testNeverRequestedIsNotRequested() {
        XCTAssertEqual(classify(.authorized, .notSupported), .notRequested)
        XCTAssertEqual(classify(.authorized, .notSupported, entitlementGranted: true), .notRequested)
        XCTAssertEqual(classify(.notDetermined, .notSupported), .notRequested)
    }

    func testRequestedWithoutEntitlementIsUnavailable() {
        XCTAssertEqual(classify(.authorized, .notSupported, requestedBefore: true), .unavailable)
        XCTAssertEqual(classify(.provisional, .notSupported, requestedBefore: true), .unavailable)
    }

    func testEntitlementFlipReturnsAwaitingUsersToNotRequested() {
        XCTAssertEqual(
            classify(.authorized, .notSupported, requestedBefore: true, entitlementGranted: true),
            .notRequested
        )
    }

    // MARK: - SafetyKindDisplay

    func testKindsMatchTheWireContract() {
        XCTAssertEqual(SafetyKindDisplay.all, ["smoke", "carbon_monoxide", "panic", "intrusion", "water_leak"])
    }

    func testEveryKindHasAReadableLabel() {
        for kind in SafetyKindDisplay.all {
            let label = SafetyKindDisplay.label(kind)
            XCTAssertFalse(label.isEmpty, "No label for \(kind)")
            XCTAssertFalse(label.contains("_"), "Raw wire value leaked for \(kind)")
        }
    }

    func testUnknownKindFallsBackToUnderscoreReplacement() {
        XCTAssertEqual(SafetyKindDisplay.label("gas_leak"), "gas leak")
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

    // MARK: - UpdateSafetySourceRequest encoding

    func testNilNameIsOmittedFromTheWire() throws {
        let data = try JSONEncoder().encode(UpdateSafetySourceRequest(name: nil, criticalEnabled: true))
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(object.keys.sorted(), ["critical_enabled"])
        XCTAssertEqual(object["critical_enabled"] as? Bool, true)
    }

    func testNilCriticalEnabledIsOmittedFromTheWire() throws {
        let data = try JSONEncoder().encode(UpdateSafetySourceRequest(name: "Hall detector", criticalEnabled: nil))
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(object.keys.sorted(), ["name"])
        XCTAssertEqual(object["name"] as? String, "Hall detector")
    }
}

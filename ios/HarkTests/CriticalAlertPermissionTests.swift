//
//  CriticalAlertPermissionTests.swift
//  HarkTests
//
//  Critical Alert helper tests.
//

import UserNotifications
import XCTest
@testable import Hark

final class CriticalAlertPermissionTests: XCTestCase {

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

    // MARK: - Critical service request encoding

    func testCreateServiceIncludesAllDefaultsAndCriticalSwitch() throws {
        let data = try JSONEncoder().encode(CreateCriticalServiceRequest(
            title: "Home Assistant",
            imageUrl: nil,
            url: nil,
            priority: "normal",
            criticalEnabled: true
        ))
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(object.keys.sorted(), ["critical_enabled", "priority", "title"])
        XCTAssertEqual(object["title"] as? String, "Home Assistant")
        XCTAssertEqual(object["priority"] as? String, "normal")
        XCTAssertEqual(object["critical_enabled"] as? Bool, true)
    }

    func testUpdateCarriesTheSameDefaultsAsAService() throws {
        let data = try JSONEncoder().encode(UpdateCriticalServiceRequest(
            title: "Front door",
            imageUrl: "https://example.com/front-door.png",
            url: "hark-test://front-door",
            priority: "critical",
            criticalEnabled: true
        ))
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(object.keys.sorted(), ["critical_enabled", "image_url", "priority", "title", "url"])
        XCTAssertEqual(object["image_url"] as? String, "https://example.com/front-door.png")
        XCTAssertEqual(object["url"] as? String, "hark-test://front-door")
        XCTAssertEqual(object["priority"] as? String, "critical")
    }

    func testUpdateCanClearOptionalDefaults() throws {
        let data = try JSONEncoder().encode(UpdateCriticalServiceRequest(
            title: "Front door",
            imageUrl: nil,
            url: nil,
            priority: "time_sensitive",
            criticalEnabled: false
        ))
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertTrue(object["image_url"] is NSNull)
        XCTAssertTrue(object["url"] is NSNull)
        XCTAssertEqual(object["critical_enabled"] as? Bool, false)
    }
}

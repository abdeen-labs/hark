//
//  AppLockTests.swift
//  HarkTests
//
//  App lock state machine tests, through the injected authorization.
//

import SwiftUI
import XCTest
@testable import Hark

@MainActor
final class AppLockTests: XCTestCase {
    private static let suiteName = "AppLockTests"

    private func freshDefaults() -> UserDefaults {
        let defaults = UserDefaults(suiteName: Self.suiteName)!
        defaults.removePersistentDomain(forName: Self.suiteName)
        return defaults
    }

    private func drain() async {
        for _ in 0 ..< 20 { await Task.yield() }
    }

    private final class Tally {
        var count = 0
    }

    // MARK: - Init

    func testStartsUnlockedWhenDisabled() {
        let lock = AppLock(defaults: freshDefaults())
        XCTAssertFalse(lock.isEnabled)
        XCTAssertFalse(lock.isLocked)
    }

    func testStartsLockedWhenEnabled() {
        let defaults = freshDefaults()
        defaults.set(true, forKey: AppLock.enabledKey)
        let lock = AppLock(defaults: defaults)
        XCTAssertTrue(lock.isEnabled)
        XCTAssertTrue(lock.isLocked)
    }

    // MARK: - Enabling and disabling

    func testEnableRequiresAuthorization() async {
        let defaults = freshDefaults()
        let lock = AppLock(defaults: defaults)

        lock.authorize = { false }
        let denied = await lock.setEnabled(true)
        XCTAssertFalse(denied)
        XCTAssertFalse(lock.isEnabled)
        XCTAssertFalse(defaults.bool(forKey: AppLock.enabledKey))

        lock.authorize = { true }
        let granted = await lock.setEnabled(true)
        XCTAssertTrue(granted)
        XCTAssertTrue(lock.isEnabled)
        XCTAssertTrue(defaults.bool(forKey: AppLock.enabledKey))
    }

    func testDisableRequiresAuthorization() async {
        let defaults = freshDefaults()
        defaults.set(true, forKey: AppLock.enabledKey)
        let lock = AppLock(defaults: defaults)

        lock.authorize = { false }
        let denied = await lock.setEnabled(false)
        XCTAssertFalse(denied)
        XCTAssertTrue(lock.isEnabled)
        XCTAssertTrue(defaults.bool(forKey: AppLock.enabledKey))

        lock.authorize = { true }
        let granted = await lock.setEnabled(false)
        XCTAssertTrue(granted)
        XCTAssertFalse(lock.isEnabled)
        XCTAssertFalse(lock.isLocked)
        XCTAssertFalse(defaults.bool(forKey: AppLock.enabledKey))
    }

    func testSettingTheCurrentValueNeedsNoAuthorization() async {
        let lock = AppLock(defaults: freshDefaults())
        lock.authorize = {
            XCTFail("No authorization should run for a no-op change")
            return false
        }
        let result = await lock.setEnabled(false)
        XCTAssertTrue(result)
    }

    // MARK: - Locking

    func testBackgroundLocksWhenEnabled() async {
        let defaults = freshDefaults()
        defaults.set(true, forKey: AppLock.enabledKey)
        let lock = AppLock(defaults: defaults)
        lock.authorize = { true }
        await lock.unlock()
        XCTAssertFalse(lock.isLocked)

        lock.handleScenePhase(.background)
        XCTAssertTrue(lock.isLocked)
    }

    func testBackgroundDoesNotLockWhenDisabled() {
        let lock = AppLock(defaults: freshDefaults())
        lock.handleScenePhase(.background)
        XCTAssertFalse(lock.isLocked)
    }

    // MARK: - Unlocking

    func testUnlockClearsTheLock() async {
        let defaults = freshDefaults()
        defaults.set(true, forKey: AppLock.enabledKey)
        let lock = AppLock(defaults: defaults)
        lock.authorize = { true }
        await lock.unlock()
        XCTAssertFalse(lock.isLocked)
    }

    func testCancelledUnlockStaysLocked() async {
        let defaults = freshDefaults()
        defaults.set(true, forKey: AppLock.enabledKey)
        let lock = AppLock(defaults: defaults)
        lock.authorize = { false }
        await lock.unlock()
        XCTAssertTrue(lock.isLocked)
    }

    // MARK: - Auto-prompt

    func testActiveAutoPromptsOnceWhileLocked() async {
        let defaults = freshDefaults()
        defaults.set(true, forKey: AppLock.enabledKey)
        let lock = AppLock(defaults: defaults)
        let tally = Tally()
        lock.authorize = {
            tally.count += 1
            return true
        }

        lock.handleScenePhase(.active)
        lock.handleScenePhase(.active)
        await drain()
        XCTAssertEqual(tally.count, 1)
        XCTAssertFalse(lock.isLocked)
    }

    func testDeclinedPromptDoesNotLoopUntilTheNextLock() async {
        let defaults = freshDefaults()
        defaults.set(true, forKey: AppLock.enabledKey)
        let lock = AppLock(defaults: defaults)
        let tally = Tally()
        lock.authorize = {
            tally.count += 1
            return false
        }

        lock.handleScenePhase(.active)
        await drain()
        XCTAssertEqual(tally.count, 1)
        XCTAssertTrue(lock.isLocked)

        lock.handleScenePhase(.active)
        await drain()
        XCTAssertEqual(tally.count, 1)

        lock.handleScenePhase(.background)
        lock.handleScenePhase(.active)
        await drain()
        XCTAssertEqual(tally.count, 2)
    }

    func testActiveDoesNotPromptWhileUnlocked() async {
        let lock = AppLock(defaults: freshDefaults())
        lock.authorize = {
            XCTFail("No prompt should run while unlocked")
            return false
        }
        lock.handleScenePhase(.active)
        await drain()
        XCTAssertFalse(lock.isLocked)
    }

    // MARK: - Shield

    func testShieldCoversInactiveScenesOnlyWhileEnabled() async {
        let defaults = freshDefaults()
        defaults.set(true, forKey: AppLock.enabledKey)
        let lock = AppLock(defaults: defaults)
        XCTAssertFalse(lock.showsShield)

        lock.handleScenePhase(.inactive)
        XCTAssertTrue(lock.showsShield)

        lock.handleScenePhase(.background)
        XCTAssertTrue(lock.showsShield)

        lock.authorize = { true }
        lock.handleScenePhase(.active)
        await drain()
        XCTAssertFalse(lock.showsShield)

        _ = await lock.setEnabled(false)
        lock.handleScenePhase(.inactive)
        XCTAssertFalse(lock.showsShield)
    }
}

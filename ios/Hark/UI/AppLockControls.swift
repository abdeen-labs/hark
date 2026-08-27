//
//  AppLockControls.swift
//  Hark
//

import SwiftUI

struct AppLockModule: View {
    let index: String

    @Environment(AppLock.self) private var lock

    @State private var busy = false
    @State private var authFailed = false

    var body: some View {
        Module(index: index, label: "App Lock", flush: true) {
            VStack(alignment: .leading, spacing: 0) {
                AxisToggle(
                    "Require Face ID",
                    sub: "Use Face ID or your passcode each time you reopen Hark.",
                    busy: busy,
                    disabled: lock.unavailableReason != nil,
                    isOn: lock.isEnabled
                ) { enabled in
                    toggle(enabled)
                }
                .padding(.vertical, 12)
                if let reason = lock.unavailableReason {
                    Notice(kind: .warn, message: reason)
                        .padding(.bottom, 16)
                } else if authFailed {
                    Notice(kind: .error, message: "Couldn't authenticate. App Lock wasn't changed.")
                        .padding(.bottom, 16)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 4)
        } trailing: {
            HStack(spacing: 8) {
                StatusLight(color: lock.isEnabled ? Axis.ok : Axis.inkFaint, size: 5)
                Meta(lock.isEnabled ? "On" : "Off", color: Axis.inkSubtle)
            }
        }
    }

    private func toggle(_ enabled: Bool) {
        guard !busy else { return }
        busy = true
        authFailed = false
        Task {
            authFailed = !(await lock.setEnabled(enabled))
            busy = false
        }
    }
}

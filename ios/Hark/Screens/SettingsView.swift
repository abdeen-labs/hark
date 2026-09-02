//
//  SettingsView.swift
//  Hark
//
//  Account, connection, Critical Alerts, and session settings.
//

import SwiftUI

nonisolated enum SettingsRoute: Hashable {
    case criticalServices
    case sound
}

struct SettingsView: View {
    @Environment(AppModel.self) private var model

    @State private var signingOut = false

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                head
                Hairline()
                ScrollView {
                    VStack(alignment: .leading, spacing: 24) {
                        route
                            .padding(.top, 24)
                        account
                        connection
                        SoundModule(index: "03", route: SettingsRoute.sound)
                        criticalAlerts
                        AppearanceModule(index: "05")
                        AppLockModule(index: "06")
                        session
                        foot
                    }
                    .padding(.horizontal, Axis.gutter)
                    .padding(.bottom, 24)
                }
            }
            .shellInsets()
            .toolbarVisibility(.hidden, for: .navigationBar)
            .navigationDestination(for: SettingsRoute.self) { route in
                switch route {
                case .criticalServices:
                    CriticalServicesView()
                        .shellInsets()
                case .sound:
                    SoundPickerView()
                        .shellInsets()
                }
            }
            .task {
                await model.refreshCriticalSettings()
                await model.refreshNotificationPermission()
            }
        }
    }

    private var head: some View {
        VStack(alignment: .leading, spacing: 0) {
            Eyebrow(index: "04", label: "Spec")
            .padding(.top, 20)
            DisplayTitle(text: "Settings", size: 56)
                .padding(.top, 28)
                .padding(.bottom, 18)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Axis.gutter)
    }

    private var route: some View {
        Module(index: "00", label: "Route", variant: .flat, flush: true) {
            Schematic(
                nodes: ["Agent", "harkd", "APNs", "This phone"],
                signalSegment: 2,
                note: AppModel.apnsEnvironment,
                height: 60
            )
            .padding(.vertical, 10)
        } trailing: {
            Meta("Direct APNs")
        }
    }

    private var account: some View {
        Module(index: "01", label: "Account", flush: true) {
            VStack(spacing: 0) {
                SpecRow(label: "Username", value: model.user?.username ?? "—")
                if let email = model.user?.email, !email.isEmpty {
                    Hairline(color: Axis.lineFaint)
                    SpecRow(label: "Email", value: email)
                }
                if let created = model.user?.createdAt {
                    Hairline(color: Axis.lineFaint)
                    SpecRow(label: "Member since", value: created.formatted(date: .abbreviated, time: .omitted), mono: false)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 4)
        }
    }

    private var connection: some View {
        Module(index: "02", label: "Connection", flush: true) {
            VStack(spacing: 0) {
                SpecRow(label: "Server", value: model.serverURL.absoluteString)
                if let expires = model.sessionInfo?.expiresAt {
                    Hairline(color: Axis.lineFaint)
                    SpecRow(label: "Session until", value: expires.formatted(date: .abbreviated, time: .shortened), mono: false)
                }
                Hairline(color: Axis.lineFaint)
                SpecRow(label: "APNs", value: AppModel.apnsEnvironment)
                if let deviceID = model.deviceID {
                    Hairline(color: Axis.lineFaint)
                    SpecRow(label: "Device", value: deviceID, expandable: true)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 4)
        } trailing: {
            HStack(spacing: 8) {
                StatusLight(color: model.deviceID != nil ? Axis.ok : Axis.warn, size: 5, rotated: model.deviceID == nil)
                Meta(model.deviceID != nil ? "Registered" : "Registering", color: Axis.inkSubtle)
            }
        }
    }

    private var criticalAlerts: some View {
        Module(index: "04", label: "Critical Alerts", flush: true) {
            VStack(spacing: 0) {
                AxisToggle(
                    "Critical Alerts",
                    sub: "Allow Critical priority when each service's switch is also on. Other priorities are unchanged.",
                    busy: model.criticalSettings == nil,
                    isOn: model.criticalSettings?.criticalAlertsEnabled ?? false
                ) { enabled in
                    Task { await model.setCriticalAlertsEnabled(enabled) }
                }
                .padding(.vertical, 12)
                Hairline(color: Axis.lineFaint)
                SpecRow(label: "This phone", value: permissionWord, mono: false)
                Hairline(color: Axis.lineFaint)
                NavigationLink(value: SettingsRoute.criticalServices) {
                    HStack(spacing: 16) {
                        Meta("Services")
                            .frame(width: 100, alignment: .leading)
                        Text("Manage critical services")
                            .font(AxisType.copy(14))
                            .foregroundStyle(Axis.inkMuted)
                        Spacer(minLength: 8)
                        Text("→")
                            .axisControl(12)
                            .foregroundStyle(Axis.inkSubtle)
                    }
                    .padding(.vertical, 9)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 4)
        } trailing: {
            HStack(spacing: 8) {
                StatusLight(color: criticalAlertsLight.color, size: 5, rotated: criticalAlertsLight.rotated)
                Meta(criticalAlertsWord, color: Axis.inkSubtle)
            }
        }
    }

    private var permissionWord: String {
        switch model.criticalAlertState {
        case .unknown: "Checking"
        case .notificationsDenied: "Notifications off"
        case .notRequested: "Not set up"
        case .granted: "Granted"
        case .criticalDenied: "Denied"
        }
    }

    private var criticalAlertsLight: (color: Color, rotated: Bool) {
        switch model.criticalAlertState {
        case .granted:
            model.criticalSettings?.criticalAlertsEnabled == true ? (Axis.ok, false) : (Axis.warn, true)
        case .criticalDenied, .notificationsDenied:
            (Axis.alarm, false)
        default:
            (Axis.warn, true)
        }
    }

    private var criticalAlertsWord: String {
        switch model.criticalAlertState {
        case .granted:
            model.criticalSettings?.criticalAlertsEnabled == true ? "Ready" : "Off"
        case .criticalDenied, .notificationsDenied:
            "Denied"
        default:
            "Setup"
        }
    }

    private var session: some View {
        Module(index: "07", label: "Session", variant: .warning) {
            VStack(alignment: .leading, spacing: 16) {
                Text("Signing out does not unregister this device.")
                    .font(AxisType.copy(13))
                    .foregroundStyle(Axis.inkSubtle)
                    .fixedSize(horizontal: false, vertical: true)
                Button(signingOut ? "Signing out…" : "Sign out") {
                    guard !signingOut else { return }
                    signingOut = true
                    Task {
                        await model.signOut()
                        signingOut = false
                    }
                }
                .buttonStyle(.instrument(.danger, fill: false))
                .disabled(signingOut)
            }
        }
    }

    private var foot: some View {
        HStack {
            Meta("Hark · Rev \(AppInfo.version)", color: Axis.inkFaint)
            Spacer()
            Lockup()
        }
        .padding(.top, 14)
        .overlay(alignment: .top) { Hairline() }
    }
}

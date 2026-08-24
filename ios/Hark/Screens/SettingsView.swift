//
//  SettingsView.swift
//  Hark
//
//  The spec sheet for this installation: the route a push takes, the
//  account, the connection, and the one hazardous control. No password
//  change UI — that is the dashboard's job.
//

import SwiftUI

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
                        session
                        foot
                    }
                    .padding(.horizontal, Axis.gutter)
                    .padding(.bottom, 24)
                }
            }
            .shellInsets()
            .toolbarVisibility(.hidden, for: .navigationBar)
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
                StatusLight(color: model.deviceID != nil ? Axis.ok : Axis.warn, size: 5)
                Meta(model.deviceID != nil ? "Registered" : "Registering", color: Axis.inkSubtle)
            }
        }
    }

    private var session: some View {
        Module(index: "03", label: "Session", variant: .hazard) {
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
        HStack(alignment: .firstTextBaseline) {
            Meta("Hark · Rev \(AppInfo.version)", color: Axis.inkFaint)
            Spacer()
            Text("Abdeen Labs")
                .font(AxisType.meta(11))
                .tracking(AxisType.tracking(AxisType.wordmarkTracking, at: 11))
                .textCase(.uppercase)
                .foregroundStyle(Axis.inkFaint)
        }
        .padding(.top, 14)
        .overlay(alignment: .top) { Hairline() }
    }
}

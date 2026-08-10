//
//  SettingsView.swift
//  Hark
//
//  Account, session, server, sign out. No password change UI — that is the
//  dashboard's job.
//

import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var model

    @State private var signingOut = false

    var body: some View {
        NavigationStack {
            List {
                Section {
                    row(label: "Username", value: model.user?.username ?? "—")
                    if let email = model.user?.email, !email.isEmpty {
                        row(label: "Email", value: email)
                    }
                    if let created = model.user?.createdAt {
                        row(label: "Member since", value: created.formatted(date: .abbreviated, time: .omitted))
                    }
                } header: {
                    AxisSectionHeader(title: "Account")
                }
                .listRowBackground(Axis.plate)
                .listRowSeparatorTint(Axis.stroke)

                Section {
                    row(label: "Server", value: model.serverURL.absoluteString)
                    if let expires = model.sessionInfo?.expiresAt {
                        row(label: "Session slides to", value: expires.formatted(date: .abbreviated, time: .shortened))
                    }
                    row(label: "Push environment", value: AppModel.apnsEnvironment)
                    if let deviceID = model.deviceID {
                        row(label: "Device ID", value: deviceID, mono: true)
                    }
                } header: {
                    AxisSectionHeader(title: "Connection")
                } footer: {
                    Text("Sessions slide forward with use and never need renewing while the app is opened at least monthly.")
                        .font(.caption2)
                        .foregroundStyle(Axis.textTertiary)
                }
                .listRowBackground(Axis.plate)
                .listRowSeparatorTint(Axis.stroke)

                Section {
                    Button {
                        guard !signingOut else { return }
                        signingOut = true
                        Task {
                            await model.signOut()
                            signingOut = false
                        }
                    } label: {
                        HStack {
                            Spacer()
                            Text("Sign out")
                                .font(.subheadline.weight(.semibold))
                                .foregroundStyle(Axis.accent)
                            Spacer()
                        }
                    }
                    .disabled(signingOut)
                }
                .listRowBackground(Axis.plate)

                Section {
                    HStack {
                        Spacer()
                        Text("Hark \(Self.versionString)")
                            .font(.caption2)
                            .monospacedDigit()
                            .foregroundStyle(Axis.textTertiary)
                        Spacer()
                    }
                    .listRowBackground(Color.clear)
                }
            }
            .listStyle(.insetGrouped)
            .scrollContentBackground(.hidden)
            .background(Axis.bg)
            .navigationTitle("Settings")
        }
    }

    private func row(label: String, value: String, mono: Bool = false) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label)
                .font(.subheadline)
                .foregroundStyle(Axis.textSecondary)
            Spacer()
            Text(value)
                .font(mono ? .caption.monospaced() : .subheadline)
                .foregroundStyle(Axis.textPrimary)
                .multilineTextAlignment(.trailing)
                .textSelection(.enabled)
        }
    }

    private static var versionString: String {
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "1.0"
        let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "1"
        return "\(version) (\(build))"
    }
}

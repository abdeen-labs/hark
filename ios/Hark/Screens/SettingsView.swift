//
//  SettingsView.swift
//  Hark
//
//  Account, session, server, sign out. No password change UI — that is the
//  dashboard's job.
//

import SwiftUI
import UIKit

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

    /// A label/value row. Long-press copies the value; text selection inside a
    /// List row loses the gesture fight with the row itself, so the context
    /// menu is the copy affordance. A mono value is an identifier that never
    /// fits beside its label — it gets the full row width and wraps.
    private func row(label: String, value: String, mono: Bool = false) -> some View {
        Group {
            if mono {
                VStack(alignment: .leading, spacing: 3) {
                    Text(label)
                        .font(.subheadline)
                        .foregroundStyle(Axis.textSecondary)
                    Text(value)
                        .font(.caption.monospaced())
                        .foregroundStyle(Axis.textPrimary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            } else {
                HStack(alignment: .firstTextBaseline) {
                    Text(label)
                        .font(.subheadline)
                        .foregroundStyle(Axis.textSecondary)
                    Spacer()
                    Text(value)
                        .font(.subheadline)
                        .foregroundStyle(Axis.textPrimary)
                        .multilineTextAlignment(.trailing)
                }
            }
        }
        .contentShape(Rectangle())
        .contextMenu {
            Button("Copy", systemImage: "doc.on.doc") {
                UIPasteboard.general.string = value
            }
        }
    }

    private static var versionString: String {
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "1.0"
        let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "1"
        return "\(version) (\(build))"
    }
}

//
//  SignInView.swift
//  Hark
//
//  The gate. Server, username, password — Hark serves exactly one account,
//  and this screen is the whole front door. The name at display scale is the
//  composition; the access panel sits under it.
//

import SwiftUI

struct SignInView: View {
    @Environment(AppModel.self) private var model

    @State private var serverText: String = AppModel.shared.serverURL.absoluteString
    @State private var username = ""
    @State private var password = ""
    @State private var busy = false
    @State private var errorMessage: String?

    @FocusState private var focused: Field?

    private enum Field {
        case server, username, password
    }

    private static let titleSize: CGFloat = 132

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                brand
                    .padding(.top, 24)
                    .padding(.bottom, 36)
                panel
                footer
                    .padding(.top, 40)
                    .padding(.bottom, 24)
            }
            .padding(.horizontal, Axis.gutter)
        }
        .scrollDismissesKeyboard(.interactively)
        .scrollIndicators(.hidden)
    }

    private var brand: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack(spacing: 8) {
                IndexLabel("Hark")
                Meta("Push relay · Self-hosted")
            }
            Text("Hark")
                .axisDisplay(Self.titleSize)
                .foregroundStyle(Axis.ink)
                .lineLimit(1)
                .fixedSize()
                .padding(.top, -Self.titleSize * 0.2)
                .padding(.bottom, -Self.titleSize * 0.14)
                .offset(x: -Self.titleSize * 0.05)
                .accessibilityAddTraits(.isHeader)
            Text("Webhooks and agent calls, delivered to this phone as push notifications, Live Activities and questions answered from the Lock Screen.")
                .font(AxisType.copy(15))
                .lineSpacing(3)
                .foregroundStyle(Axis.inkSubtle)
                .fixedSize(horizontal: false, vertical: true)
            Meta("Single account · Direct APNs · One binary")
        }
    }

    private var panel: some View {
        Module(label: "Access", variant: .marked) {
            VStack(alignment: .leading, spacing: 20) {
                FieldFrame(label: "Server", focused: focused == .server) {
                    TextField("https://hark.abdeen.dev", text: $serverText)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                        .focused($focused, equals: .server)
                        .submitLabel(.next)
                        .onSubmit { focused = .username }
                }
                FieldFrame(label: "Username", focused: focused == .username) {
                    TextField("Username", text: $username)
                        .textContentType(.username)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                        .focused($focused, equals: .username)
                        .submitLabel(.next)
                        .onSubmit { focused = .password }
                }
                FieldFrame(label: "Password", focused: focused == .password) {
                    SecureField("Password", text: $password)
                        .textContentType(.password)
                        .focused($focused, equals: .password)
                        .submitLabel(.go)
                        .onSubmit { submit() }
                }

                if let errorMessage {
                    Notice(kind: .error, message: errorMessage)
                }

                Button(busy ? "Signing in…" : "Sign in") {
                    submit()
                }
                .buttonStyle(.instrument(.primary, arrow: .forward))
                .disabled(username.isEmpty || password.isEmpty || serverText.isEmpty)
                .opacity(busy ? 0.7 : 1)
                .padding(.top, 4)
            }
        } trailing: {
            Meta("One account")
        }
    }

    private var footer: some View {
        HStack(alignment: .firstTextBaseline) {
            Text("Abdeen Labs")
                .font(AxisType.meta(11))
                .tracking(AxisType.tracking(AxisType.wordmarkTracking, at: 11))
                .textCase(.uppercase)
                .foregroundStyle(Axis.inkFaint)
            Spacer()
            Meta("Hark · Rev \(AppInfo.version)", color: Axis.inkFaint)
        }
        .padding(.top, 14)
        .overlay(alignment: .top) { Hairline() }
    }

    private func submit() {
        guard !busy else { return }
        busy = true
        errorMessage = nil
        let server = serverText
        let name = username
        let pass = password
        Task {
            defer { busy = false }
            do {
                try await model.signIn(serverText: server, username: name, password: pass)
            } catch let error as HarkClientError {
                errorMessage = signInMessage(for: error)
            } catch {
                errorMessage = (error as NSError).localizedDescription
            }
        }
    }

    private func signInMessage(for error: HarkClientError) -> String {
        if case .api(let status, let code, let message, _) = error {
            switch (status, code) {
            case (401, _):
                return "That username and password were not accepted."
            case (429, _):
                return "Too many attempts. Wait a moment and try again."
            default:
                return message
            }
        }
        return error.errorDescription ?? "Sign-in failed."
    }
}

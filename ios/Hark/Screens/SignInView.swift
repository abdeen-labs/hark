//
//  SignInView.swift
//  Hark
//
//  Server, username, password. Nothing else — Hark serves exactly one
//  account, and this screen is the whole front door.
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

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 28) {
                brand
                    .padding(.top, 64)

                VStack(alignment: .leading, spacing: 14) {
                    AxisSectionHeader(title: "Server")
                    field {
                        TextField("https://hark.abdeen.dev", text: $serverText)
                            .keyboardType(.URL)
                            .textContentType(.URL)
                            .autocorrectionDisabled()
                            .textInputAutocapitalization(.never)
                            .focused($focused, equals: .server)
                            .submitLabel(.next)
                            .onSubmit { focused = .username }
                    }

                    AxisSectionHeader(title: "Account")
                    field {
                        TextField("Username", text: $username)
                            .textContentType(.username)
                            .autocorrectionDisabled()
                            .textInputAutocapitalization(.never)
                            .focused($focused, equals: .username)
                            .submitLabel(.next)
                            .onSubmit { focused = .password }
                    }
                    field {
                        SecureField("Password", text: $password)
                            .textContentType(.password)
                            .focused($focused, equals: .password)
                            .submitLabel(.go)
                            .onSubmit { submit() }
                    }
                }

                if let errorMessage {
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundStyle(Axis.accent)
                }

                Button {
                    submit()
                } label: {
                    if busy {
                        ProgressView()
                            .tint(.white)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 2)
                    } else {
                        Text("Sign in")
                    }
                }
                .buttonStyle(AxisPrimaryButtonStyle())
                .disabled(busy || username.isEmpty || password.isEmpty || serverText.isEmpty)
                .opacity(busy || username.isEmpty || password.isEmpty ? 0.6 : 1)

                Spacer(minLength: 40)
            }
            .padding(.horizontal, 24)
        }
        .scrollDismissesKeyboard(.interactively)
        .background(Axis.bg)
    }

    private var brand: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("HARK")
                .font(.system(size: 34, weight: .bold, design: .default))
                .kerning(6)
                .foregroundStyle(Axis.textPrimary)
            Rectangle()
                .fill(Axis.accent)
                .frame(width: 44, height: 3)
            Text("Notifications for your agents.")
                .font(.footnote)
                .foregroundStyle(Axis.textSecondary)
        }
    }

    private func field(@ViewBuilder content: () -> some View) -> some View {
        content()
            .font(.body)
            .foregroundStyle(Axis.textPrimary)
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .plate()
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

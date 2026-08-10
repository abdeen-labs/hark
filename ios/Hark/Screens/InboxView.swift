//
//  InboxView.swift
//  Hark
//
//  Pending questions, answerable in place. GET /v1/interactions?status=pending.
//

import SwiftUI

struct InboxView: View {
    @Environment(AppModel.self) private var model

    @State private var alertMessage: String?

    var body: some View {
        NavigationStack {
            Group {
                if model.inbox.isEmpty {
                    ScrollView {
                        if let error = model.inboxError {
                            Text(error)
                                .font(.footnote)
                                .foregroundStyle(Axis.accent)
                                .padding()
                        }
                        AxisEmptyState(
                            icon: "tray",
                            title: "Nothing waiting",
                            detail: "Questions your agents ask will land here."
                        )
                    }
                    .refreshable { await model.refreshInbox() }
                } else {
                    List {
                        ForEach(model.inbox) { entry in
                            InboxRow(entry: entry) { message in
                                alertMessage = message
                            }
                            .listRowBackground(Axis.plate)
                            .listRowSeparatorTint(Axis.stroke)
                            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                                Button("Cancel", role: .destructive) {
                                    Task {
                                        do { try await model.cancel(entry) } catch {
                                            alertMessage = friendly(error)
                                        }
                                    }
                                }
                            }
                        }
                    }
                    .listStyle(.insetGrouped)
                    .scrollContentBackground(.hidden)
                    .refreshable { await model.refreshInbox() }
                }
            }
            .background(Axis.bg)
            .navigationTitle("Inbox")
            .task { await model.refreshInbox() }
            .alert("Inbox", isPresented: .init(
                get: { alertMessage != nil },
                set: { if !$0 { alertMessage = nil } }
            )) {
                Button("OK", role: .cancel) {}
            } message: {
                Text(alertMessage ?? "")
            }
        }
    }

    private func friendly(_ error: Error) -> String {
        (error as? HarkClientError)?.errorDescription ?? (error as NSError).localizedDescription
    }
}

struct InboxRow: View {
    @Environment(AppModel.self) private var model
    let entry: InboxEntry
    let onError: (String) -> Void

    @State private var answering = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            NavigationLink {
                InteractionDetailView(entry: entry)
            } label: {
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 8) {
                        Image(systemName: InteractionKindDisplay.icon(entry.interaction.kind))
                            .font(.caption)
                            .foregroundStyle(Axis.accent)
                        Text(entry.sourceName ?? entry.interaction.title)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(Axis.textSecondary)
                        Spacer()
                        AxisChip(text: InteractionKindDisplay.label(entry.interaction.kind))
                        ExpiryCountdown(expiresAt: entry.interaction.expiresAt)
                    }
                    Text(entry.interaction.prompt)
                        .font(.subheadline)
                        .foregroundStyle(Axis.textPrimary)
                        .lineLimit(3)
                }
            }
            .buttonStyle(.plain)

            if !entry.interaction.answerChoices.isEmpty {
                HStack(spacing: 8) {
                    ForEach(entry.interaction.answerChoices, id: \.action) { choice in
                        if choice.action == entry.interaction.answerChoices.first?.action {
                            Button(choice.label) { answer(choice.action) }
                                .buttonStyle(AxisPrimaryButtonStyle())
                                .disabled(answering)
                        } else {
                            Button(choice.label) { answer(choice.action) }
                                .buttonStyle(AxisSecondaryButtonStyle())
                                .disabled(answering)
                        }
                    }
                }
            }
        }
        .padding(.vertical, 4)
        .opacity(answering ? 0.6 : 1)
    }

    private func answer(_ action: String) {
        guard !answering else { return }
        answering = true
        Task {
            defer { answering = false }
            do {
                _ = try await model.answer(entry, action: action)
            } catch let error as HarkClientError {
                if case .api(_, "action_digest_mismatch", _, _) = error {
                    onError("This question changed since it was delivered. The list has been refreshed.")
                    await model.refreshInbox()
                } else if case .api(_, "conflict", _, _) = error {
                    onError("Already settled elsewhere. The list has been refreshed.")
                    await model.refreshInbox()
                } else {
                    onError(error.errorDescription ?? "Answering failed.")
                }
            } catch {
                onError((error as NSError).localizedDescription)
            }
        }
    }
}

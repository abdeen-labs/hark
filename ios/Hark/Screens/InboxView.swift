//
//  InboxView.swift
//  Hark
//
//  Pending questions, answerable in place. GET /v1/interactions?status=pending.
//  The count is the page's anchor; the questions read as a ledger under it.
//

import SwiftUI

struct InboxView: View {
    @Environment(AppModel.self) private var model

    @State private var alertMessage: String?

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                head
                Hairline()
                content
            }
            .shellInsets()
            .toolbarVisibility(.hidden, for: .navigationBar)
            .navigationDestination(for: InboxEntry.self) { entry in
                InteractionDetailView(entry: entry)
                    .shellInsets()
            }
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

    private var count: Int { model.inbox.count }

    private var head: some View {
        VStack(alignment: .leading, spacing: 0) {
            Eyebrow(index: "01", label: "Queue · Pending") {
                if let refreshed = model.inboxRefreshedAt {
                    Meta("Sync \(AxisClock.stamp(refreshed))")
                }
            }
            .padding(.top, 20)
            Metric(
                value: String(format: "%02d", count),
                label: count == 1 ? "Question waiting" : "Questions waiting",
                size: 104,
                color: count == 0 ? Axis.inkFaint : Axis.ink
            )
            .padding(.top, 18)
            .padding(.bottom, 22)
            .animation(Axis.Motion.ease, value: count)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("\(count) \(count == 1 ? "question" : "questions") waiting")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Axis.gutter)
    }

    @ViewBuilder private var content: some View {
        if model.inbox.isEmpty {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    if let error = model.inboxError {
                        Notice(kind: .error, message: error)
                            .padding(.top, 16)
                    }
                    EmptyNote(
                        text: "Nothing waiting.",
                        detail: "Questions your agents ask land here, and on the Lock Screen."
                    )
                    Schematic(note: "Question")
                        .padding(.bottom, 12)
                    Hairline()
                }
                .padding(.horizontal, Axis.gutter)
            }
            .refreshable { await model.refreshInbox() }
        } else {
            List {
                ForEach(Array(model.inbox.enumerated()), id: \.element.id) { index, entry in
                    InboxRow(number: index + 1, entry: entry) { message in
                        alertMessage = message
                    }
                    .listRowInsets(EdgeInsets())
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        Button("Cancel", role: .destructive) {
                            Task {
                                do { try await model.cancel(entry) } catch {
                                    alertMessage = friendly(error)
                                }
                            }
                        }
                        .tint(Axis.signal)
                    }
                }
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .refreshable { await model.refreshInbox() }
        }
    }

    private func friendly(_ error: Error) -> String {
        (error as? HarkClientError)?.errorDescription ?? (error as NSError).localizedDescription
    }
}

struct InboxRow: View {
    @Environment(AppModel.self) private var model
    let number: Int
    let entry: InboxEntry
    let onError: (String) -> Void

    @State private var answering = false

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 0) {
                LedgerIndex(number: number)
                    .padding(.top, 3)
                VStack(alignment: .leading, spacing: 12) {
                    NavigationLink(value: entry) {
                        VStack(alignment: .leading, spacing: 8) {
                            HStack(spacing: 8) {
                                Text(entry.sourceName ?? entry.interaction.title)
                                    .font(AxisType.copy(13, weight: .medium))
                                    .foregroundStyle(Axis.ink)
                                    .lineLimit(1)
                                Tag(InteractionKindDisplay.label(entry.interaction.kind))
                                Spacer(minLength: 8)
                                ExpiryCountdown(expiresAt: entry.interaction.expiresAt)
                            }
                            Text(entry.interaction.prompt)
                                .font(AxisType.copy(15))
                                .lineSpacing(2)
                                .foregroundStyle(Axis.ink)
                                .lineLimit(3)
                                .fixedSize(horizontal: false, vertical: true)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)

                    controls
                }
            }
            .padding(.horizontal, Axis.gutter)
            .padding(.vertical, 14)
            Hairline()
        }
        .opacity(answering ? 0.6 : 1)
    }

    @ViewBuilder private var controls: some View {
        let choices = entry.interaction.answerChoices
        if !choices.isEmpty {
            HStack(spacing: 8) {
                ForEach(choices, id: \.action) { choice in
                    let primary = choice.action == choices.first?.action
                    Button(choice.label) { answer(choice.action) }
                        .buttonStyle(.instrument(primary ? .primary : .secondary, arrow: primary ? .forward : nil))
                        .disabled(answering)
                }
            }
        } else if entry.interaction.kind == "reply" {
            NavigationLink(value: entry) {
                Text("Reply")
            }
            .buttonStyle(.instrument(.secondary, arrow: .forward))
        }
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

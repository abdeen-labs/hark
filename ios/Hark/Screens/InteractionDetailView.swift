//
//  InteractionDetailView.swift
//  Hark
//
//  One question, watched live. Long-polls GET /v1/interactions/{id} with
//  wait_seconds=25 while the question is pending, so an answer given
//  anywhere — this phone, another device, the Lock Screen — appears the
//  moment it lands. The prompt is the page; the deadline is its clock.
//

import SwiftUI

struct InteractionDetailView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    let entry: InboxEntry

    @State private var interaction: APIInteraction
    @State private var replyText = ""
    @State private var busy = false
    @State private var errorMessage: String?
    @FocusState private var replyFocused: Bool

    init(entry: InboxEntry) {
        self.entry = entry
        _interaction = State(initialValue: entry.interaction)
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                bar
                    .padding(.top, 10)
                head
                    .padding(.top, 28)
                Hairline()
                    .padding(.top, 24)
                status
                    .padding(.top, 24)
                if interaction.isPending {
                    answers
                        .padding(.top, 24)
                }
                if let response = interaction.response, !response.isEmpty {
                    responseModule
                        .padding(.top, 24)
                }
                if let errorMessage {
                    Notice(kind: .error, message: errorMessage)
                        .padding(.top, 16)
                }
                spec
                    .padding(.top, 32)
                    .padding(.bottom, 32)
            }
            .padding(.horizontal, Axis.gutter)
        }
        .scrollDismissesKeyboard(.interactively)
        .toolbarVisibility(.hidden, for: .navigationBar)
        .task(id: interaction.id) { await watch() }
    }

    // MARK: Composition

    private var bar: some View {
        HStack {
            Button("Inbox") { dismiss() }
                .buttonStyle(.instrument(.ghost, arrow: .back, compact: true, fill: false))
                .padding(.leading, -10)
            Spacer()
            if let position {
                Meta("Q \(position)")
            }
        }
    }

    private var position: String? {
        guard let index = model.inbox.firstIndex(where: { $0.id == entry.id }) else { return nil }
        return String(format: "%02d / %02d", index + 1, model.inbox.count)
    }

    private var head: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 8) {
                Meta(InteractionKindDisplay.label(interaction.kind), color: Axis.signalText)
                Meta("·")
                Meta(entry.sourceName ?? interaction.title, color: Axis.inkSubtle)
            }
            Text(interaction.prompt)
                .axisHeadline(26)
                .foregroundStyle(Axis.ink)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityAddTraits(.isHeader)
        }
    }

    private var status: some View {
        Module(index: "01", label: "Status", variant: interaction.isPending ? .signal : .plain) {
            VStack(alignment: .leading, spacing: 10) {
                if interaction.isPending {
                    ExpiryCountdown(expiresAt: interaction.expiresAt, size: 40, color: Axis.ink)
                    Meta("Until expiry")
                } else {
                    Text(AxisState.label(interaction.status).capitalized)
                        .axisUI(28)
                        .foregroundStyle(Axis.ink)
                    if let respondedAt = interaction.respondedAt {
                        Meta("Answered \(AxisClock.age(respondedAt)) ago · \(AxisClock.short(respondedAt))")
                    } else if let canceledAt = interaction.canceledAt {
                        Meta("Canceled \(AxisClock.age(canceledAt)) ago")
                    } else {
                        Meta("Settled")
                    }
                }
            }
        } trailing: {
            StateTag(state: interaction.status)
        }
    }

    @ViewBuilder private var answers: some View {
        if interaction.kind == "reply" {
            Module(index: "02", label: "Your reply") {
                VStack(alignment: .leading, spacing: 16) {
                    FieldFrame(focused: replyFocused) {
                        TextField("Type a reply…", text: $replyText, axis: .vertical)
                            .lineLimit(3 ... 8)
                            .focused($replyFocused)
                    }
                    Button("Send reply") {
                        answer("reply", text: replyText)
                    }
                    .buttonStyle(.instrument(.primary, arrow: .forward))
                    .disabled(busy || replyText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        } else {
            let choices = interaction.answerChoices
            HStack(spacing: 8) {
                ForEach(choices, id: \.action) { choice in
                    let primary = choice.action == choices.first?.action
                    Button(choice.label) { answer(choice.action) }
                        .buttonStyle(.instrument(primary ? .primary : .secondary, arrow: primary ? .forward : nil))
                        .disabled(busy)
                }
            }
        }
    }

    private var responseModule: some View {
        Module(index: "02", label: "Response") {
            Text(interaction.response ?? "")
                .font(AxisType.copy(15))
                .lineSpacing(2)
                .foregroundStyle(Axis.inkMuted)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var spec: some View {
        VStack(spacing: 0) {
            Hairline()
            SpecRow(label: "Asked", value: "\(AxisClock.stamp(interaction.createdAt)) · \(interaction.createdAt.formatted(.dateTime.day(.twoDigits).month(.abbreviated)))")
            Hairline(color: Axis.lineFaint)
            SpecRow(label: "Expires", value: "\(AxisClock.stamp(interaction.expiresAt)) · \(interaction.expiresAt.formatted(.dateTime.day(.twoDigits).month(.abbreviated)))")
            Hairline(color: Axis.lineFaint)
            SpecRow(label: "Question", value: interaction.id, expandable: true)
            if let correlation = interaction.correlationId, !correlation.isEmpty {
                Hairline(color: Axis.lineFaint)
                SpecRow(label: "Correlation", value: correlation, expandable: true)
            }
            Hairline()
        }
    }

    // MARK: Network

    /// The long-poll loop. Each request is held open for up to 25 seconds;
    /// the moment the question settles, the new state comes back.
    private func watch() async {
        while !Task.isCancelled, interaction.isPending, interaction.expiresAt > .now {
            do {
                let updated = try await model.client.interaction(id: interaction.id, waitSeconds: 25)
                interaction = updated
                if !updated.isPending {
                    await model.refreshInbox()
                    break
                }
            } catch let error as HarkClientError where error.isUnauthorized {
                model.handleUnauthorized()
                break
            } catch {
                if Task.isCancelled { break }
                try? await Task.sleep(for: .seconds(3))
            }
        }
    }

    private func answer(_ action: String, text: String? = nil) {
        guard !busy else { return }
        busy = true
        errorMessage = nil
        Task {
            defer { busy = false }
            do {
                let updated = try await model.answer(entry, action: action, text: text)
                withAnimation(Axis.Motion.ease) { interaction = updated }
            } catch let error as HarkClientError {
                if case .api(_, "action_digest_mismatch", _, _) = error {
                    errorMessage = "This question changed since it was delivered."
                } else {
                    errorMessage = error.errorDescription
                }
            } catch {
                errorMessage = (error as NSError).localizedDescription
            }
        }
    }
}

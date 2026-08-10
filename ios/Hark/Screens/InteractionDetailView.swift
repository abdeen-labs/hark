//
//  InteractionDetailView.swift
//  Hark
//
//  One question, watched live. Long-polls GET /v1/interactions/{id} with
//  wait_seconds=25 while the question is pending, so an answer given
//  anywhere — this phone, another device, the Lock Screen — appears the
//  moment it lands.
//

import SwiftUI

struct InteractionDetailView: View {
    @Environment(AppModel.self) private var model

    let entry: InboxEntry

    @State private var interaction: APIInteraction
    @State private var replyText = ""
    @State private var busy = false
    @State private var errorMessage: String?

    init(entry: InboxEntry) {
        self.entry = entry
        _interaction = State(initialValue: entry.interaction)
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                header
                promptPlate
                statusSection
                if interaction.isPending {
                    answerSection
                }
                if let errorMessage {
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundStyle(Axis.accent)
                }
            }
            .padding(16)
        }
        .background(Axis.bg)
        .navigationTitle("Question")
        .navigationBarTitleDisplayMode(.inline)
        .task(id: interaction.id) { await watch() }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: InteractionKindDisplay.icon(interaction.kind))
                .foregroundStyle(Axis.accent)
            Text(entry.sourceName ?? interaction.title)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(Axis.textPrimary)
            Spacer()
            AxisChip(text: InteractionKindDisplay.label(interaction.kind))
        }
    }

    private var promptPlate: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(interaction.prompt)
                .font(.body)
                .foregroundStyle(Axis.textPrimary)
                .frame(maxWidth: .infinity, alignment: .leading)
            if interaction.isPending {
                HStack(spacing: 6) {
                    Image(systemName: "hourglass")
                        .font(.caption2)
                        .foregroundStyle(Axis.textTertiary)
                    ExpiryCountdown(expiresAt: interaction.expiresAt)
                }
            }
        }
        .padding(14)
        .plate()
    }

    private var statusSection: some View {
        HStack(spacing: 10) {
            AxisSectionHeader(title: "Status")
            ResultPill(result: interaction.status)
            Spacer()
            if let respondedAt = interaction.respondedAt {
                Text(respondedAt.formatted(.relative(presentation: .named)))
                    .font(.caption)
                    .foregroundStyle(Axis.textTertiary)
            }
        }
    }

    @ViewBuilder private var answerSection: some View {
        if interaction.kind == "reply" {
            VStack(alignment: .leading, spacing: 10) {
                AxisSectionHeader(title: "Your reply")
                TextField("Type a reply…", text: $replyText, axis: .vertical)
                    .lineLimit(3 ... 8)
                    .font(.body)
                    .foregroundStyle(Axis.textPrimary)
                    .padding(12)
                    .plate()
                Button("Send reply") {
                    answer("reply", text: replyText)
                }
                .buttonStyle(AxisPrimaryButtonStyle())
                .disabled(busy || replyText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        } else {
            HStack(spacing: 10) {
                ForEach(interaction.answerChoices, id: \.action) { choice in
                    if choice.action == interaction.answerChoices.first?.action {
                        Button(choice.label) { answer(choice.action) }
                            .buttonStyle(AxisPrimaryButtonStyle())
                            .disabled(busy)
                    } else {
                        Button(choice.label) { answer(choice.action) }
                            .buttonStyle(AxisSecondaryButtonStyle())
                            .disabled(busy)
                    }
                }
            }
        }
    }

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
                interaction = updated
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

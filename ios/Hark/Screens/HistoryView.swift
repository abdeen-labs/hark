//
//  HistoryView.swift
//  Hark
//
//  Everything that has happened to the account, in one ordering.
//  GET /v1/history with kind chips, keyset pagination, swipe-to-delete.
//

import SwiftUI

struct HistoryView: View {
    @Environment(AppModel.self) private var model

    private static let kinds: [(label: String, value: String)] = [
        ("All", "all"),
        ("Notifications", "notification"),
        ("Responses", "response"),
        ("Live Activity", "live_activity"),
    ]

    @State private var kind = "all"
    @State private var items: [HistoryItem] = []
    @State private var nextCursor: String?
    @State private var loading = false
    @State private var loadedOnce = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                chips
                list
            }
            .background(Axis.bg)
            .navigationTitle("History")
            .task { if !loadedOnce { await reload() } }
        }
    }

    private var chips: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(Self.kinds, id: \.value) { entry in
                    Button {
                        guard kind != entry.value else { return }
                        kind = entry.value
                        Task { await reload() }
                    } label: {
                        Text(entry.label)
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(kind == entry.value ? Color.white : Axis.textSecondary)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 6)
                            .background(kind == entry.value ? Axis.accent : Axis.plate)
                            .clipShape(Capsule())
                            .overlay(
                                Capsule().strokeBorder(
                                    kind == entry.value ? Color.clear : Axis.stroke,
                                    lineWidth: 1
                                )
                            )
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
        }
    }

    @ViewBuilder private var list: some View {
        if items.isEmpty {
            ScrollView {
                if let errorMessage {
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundStyle(Axis.accent)
                        .padding()
                }
                if loading {
                    ProgressView().tint(Axis.accent).padding(.top, 48)
                } else if loadedOnce {
                    AxisEmptyState(
                        icon: "clock",
                        title: "Nothing yet",
                        detail: "Deliveries, answers, and Live Activity runs will collect here."
                    )
                }
            }
            .refreshable { await reload() }
        } else {
            List {
                ForEach(items) { item in
                    HistoryRow(item: item)
                        .listRowBackground(Axis.plate)
                        .listRowSeparatorTint(Axis.stroke)
                        .onAppear {
                            if item.id == items.last?.id {
                                Task { await loadMore() }
                            }
                        }
                }
                .onDelete { offsets in
                    delete(at: offsets)
                }

                if nextCursor != nil {
                    HStack {
                        Spacer()
                        ProgressView().tint(Axis.accent)
                        Spacer()
                    }
                    .listRowBackground(Color.clear)
                }
            }
            .listStyle(.insetGrouped)
            .scrollContentBackground(.hidden)
            .refreshable { await reload() }
        }
    }

    private func reload() async {
        loading = true
        defer {
            loading = false
            loadedOnce = true
        }
        do {
            let page = try await model.client.history(kind: kind)
            items = page.items
            nextCursor = page.nextCursor
            errorMessage = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    private func loadMore() async {
        guard let cursor = nextCursor, !loading else { return }
        loading = true
        defer { loading = false }
        do {
            let page = try await model.client.history(kind: kind, cursor: cursor)
            let known = Set(items.map(\.id))
            items.append(contentsOf: page.items.filter { !known.contains($0.id) })
            nextCursor = page.nextCursor
        } catch {
            // Pagination retries the next time the sentinel appears.
        }
    }

    private func delete(at offsets: IndexSet) {
        let doomed = offsets.map { items[$0] }
        items.remove(atOffsets: offsets)
        Task {
            for item in doomed {
                do {
                    try await model.client.deleteHistoryItem(id: item.id)
                } catch let error as HarkClientError where error.isUnauthorized {
                    model.handleUnauthorized()
                    return
                } catch {
                    // A pending question cannot be deleted; put the row back.
                    await reload()
                    errorMessage = (error as? HarkClientError)?.errorDescription
                    return
                }
            }
        }
    }
}

struct HistoryRow: View {
    let item: HistoryItem

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 8) {
                sourceBadge
                Text(item.sourceName ?? item.title ?? "Hark")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Axis.textSecondary)
                Spacer()
                if let result = item.result {
                    ResultPill(result: result)
                } else if let status = item.status, status != "accepted" {
                    AxisChip(text: status.replacingOccurrences(of: "_", with: " "), tint: statusTint(status))
                }
                Text(item.createdAt.formatted(.relative(presentation: .named)))
                    .font(.caption2)
                    .monospacedDigit()
                    .foregroundStyle(Axis.textTertiary)
            }
            if let detail = item.detail, !detail.isEmpty {
                Text(detail)
                    .font(.subheadline)
                    .foregroundStyle(Axis.textPrimary)
                    .lineLimit(2)
            } else if let title = item.title, item.sourceName != nil {
                Text(title)
                    .font(.subheadline)
                    .foregroundStyle(Axis.textPrimary)
                    .lineLimit(2)
            }
            if let error = item.error, !error.isEmpty {
                Text(error)
                    .font(.caption2)
                    .foregroundStyle(Axis.accent)
                    .lineLimit(2)
            }
        }
        .padding(.vertical, 3)
    }

    /// The sender's avatar when it has one, otherwise the kind glyph. A URL
    /// that fails to load falls back to the glyph rather than leaving a hole.
    private var sourceBadge: some View {
        Group {
            if let url = item.sourceImageUrl.flatMap(URL.init(string:)) {
                AsyncImage(url: url) { phase in
                    if case .success(let image) = phase {
                        image.resizable().scaledToFill()
                    } else {
                        kindGlyph
                    }
                }
                .frame(width: 18, height: 18)
                .clipShape(RoundedRectangle(cornerRadius: 5, style: .continuous))
            } else {
                kindGlyph
            }
        }
    }

    private var kindGlyph: some View {
        Image(systemName: kindIcon)
            .font(.caption)
            .foregroundStyle(kindTint)
    }

    private var kindIcon: String {
        switch item.kind {
        case "notification": "bell"
        case "response": "arrowshape.turn.up.left"
        case "live_activity": "bolt.badge.clock"
        default: "circle"
        }
    }

    private var kindTint: Color {
        switch item.kind {
        case "response": Axis.jade
        case "live_activity": Axis.amber
        default: Axis.textSecondary
        }
    }

    private func statusTint(_ status: String) -> Color {
        switch status {
        case "failed", "no_devices": Axis.accent
        case "partial": Axis.amber
        default: Axis.textSecondary
        }
    }
}

//
//  HistoryView.swift
//  Hark
//
//  Everything that has happened to the account, in one ordering.
//  GET /v1/history with kind tabs, keyset pagination, swipe-to-delete.
//  The archive reads as a ledger: a running index, the age, the entry.
//

import SwiftUI

struct HistoryView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private static let kinds: [(index: String, label: String, value: String)] = [
        ("00", "All", "all"),
        ("01", "Notifications", "notification"),
        ("02", "Responses", "response"),
        ("03", "Live Activities", "live_activity"),
    ]

    private static let indexSize: CGFloat = 200

    @State private var kind = "all"
    @State private var items: [HistoryItem] = []
    @State private var nextCursor: String?
    @State private var loading = false
    @State private var loadedOnce = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                head
                Hairline()
                tabs
                Hairline()
                list
            }
            .shellInsets()
            .toolbarVisibility(.hidden, for: .navigationBar)
            .task { if !loadedOnce { await reload() } }
        }
    }

    private var head: some View {
        VStack(alignment: .leading, spacing: 0) {
            Eyebrow(index: "02", label: "Archive")
            .padding(.top, 20)
            DisplayTitle(text: "History", size: 60)
                .padding(.top, 28)
                .padding(.bottom, 18)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Axis.gutter)
        .background(alignment: .bottomTrailing) {
            EnvironmentalIndex(text: "02", size: Self.indexSize)
                .offset(x: Self.indexSize * 0.06, y: Self.indexSize * 0.26)
        }
        .clipped()
    }

    private var tabs: some View {
        ScrollView(.horizontal) {
            HStack(spacing: 28) {
                ForEach(Self.kinds, id: \.value) { entry in
                    let active = kind == entry.value
                    Button {
                        guard kind != entry.value else { return }
                        withAnimation(reduceMotion ? nil : Axis.Motion.ease) { kind = entry.value }
                        Task { await reload() }
                    } label: {
                        HStack(spacing: 8) {
                            IndexLabel(entry.index, color: active ? Axis.signalText : Axis.inkDisabled)
                            Meta(entry.label, size: 11, color: active ? Axis.ink : Axis.inkSubtle)
                        }
                        .padding(.vertical, 16)
                        .overlay(alignment: .bottom) {
                            Rectangle()
                                .fill(Axis.signal)
                                .frame(height: 2)
                                .scaleEffect(x: active ? 1 : 0, anchor: .leading)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(active ? [.isSelected] : [])
                }
            }
            .padding(.horizontal, Axis.gutter)
        }
        .scrollIndicators(.hidden)
    }

    @ViewBuilder private var list: some View {
        if items.isEmpty {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    if let errorMessage {
                        Notice(kind: .error, message: errorMessage)
                            .padding(.top, 16)
                    }
                    if loading {
                        LoadingMark(text: "Reading the archive")
                            .padding(.vertical, 24)
                    } else if loadedOnce {
                        EmptyNote(text: kind == "all" ? "No deliveries yet." : "No matching deliveries.")
                    }
                }
                .padding(.horizontal, Axis.gutter)
            }
            .refreshable { await reload() }
        } else {
            List {
                ForEach(Array(items.enumerated()), id: \.element.id) { index, item in
                    HistoryRow(number: index + 1, item: item)
                        .listRowInsets(EdgeInsets())
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
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
                    LoadingMark(text: "Older")
                        .padding(.horizontal, Axis.gutter)
                        .padding(.vertical, 16)
                        .listRowInsets(EdgeInsets())
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                }
            }
            .listStyle(.plain)
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
    let number: Int
    let item: HistoryItem

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 0) {
                LedgerIndex(number: number)
                    .padding(.top, 2)
                Text(AxisClock.age(item.createdAt))
                    .font(AxisType.meta(11))
                    .monospacedDigit()
                    .textCase(.uppercase)
                    .foregroundStyle(Axis.inkFaint)
                    .frame(width: 40, alignment: .leading)
                    .padding(.top, 2)
                    .accessibilityLabel(item.createdAt.formatted(.relative(presentation: .named)))
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 8) {
                        avatar
                        Text(item.sourceName ?? "Hark")
                            .font(AxisType.copy(13, weight: .medium))
                            .foregroundStyle(Axis.ink)
                            .lineLimit(1)
                        Spacer(minLength: 0)
                    }
                    FlowRow {
                        Tag(AxisState.label(item.kind))
                        if let status = item.status, !status.isEmpty {
                            StateTag(state: status)
                        }
                        if let priority = item.priority, !priority.isEmpty, priority != "normal" {
                            Tag(priority, tone: .warn)
                        }
                    }
                    if let title = item.title, !title.isEmpty {
                        Text(title)
                            .font(AxisType.copy(15))
                            .foregroundStyle(Axis.ink)
                            .lineLimit(3)
                            .fixedSize(horizontal: false, vertical: true)
                            .padding(.top, 2)
                    }
                    if let detail = item.detail, !detail.isEmpty {
                        Text(detail)
                            .font(AxisType.copy(13))
                            .foregroundStyle(Axis.inkSubtle)
                            .lineLimit(2)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    if let result = item.result, !result.isEmpty {
                        Meta("Result — \(AxisState.label(result))")
                            .padding(.top, 2)
                    }
                    if let error = item.error, !error.isEmpty {
                        Text(error)
                            .font(AxisType.mono(12))
                            .foregroundStyle(Axis.danger)
                            .lineLimit(3)
                            .fixedSize(horizontal: false, vertical: true)
                            .padding(EdgeInsets(top: 6, leading: 10, bottom: 6, trailing: 10))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Axis.signalWash)
                            .overlay(alignment: .leading) {
                                Rectangle().fill(Axis.danger).frame(width: 2)
                            }
                            .padding(.top, 4)
                    }
                }
            }
            .padding(.horizontal, Axis.gutter)
            .padding(.vertical, 14)
            Hairline()
        }
    }

    /// The sender's avatar when it has one. A URL that fails to load leaves
    /// nothing behind; the name carries the row.
    @ViewBuilder private var avatar: some View {
        if let url = item.sourceImageUrl.flatMap(URL.init(string:)) {
            AsyncImage(url: url) { phase in
                if case .success(let image) = phase {
                    image
                        .resizable()
                        .scaledToFill()
                        .frame(width: 18, height: 18)
                        .clipShape(RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous))
                        .overlay(
                            RoundedRectangle(cornerRadius: Axis.Radius.xs, style: .continuous)
                                .strokeBorder(Color.white.opacity(0.1), lineWidth: 1)
                        )
                }
            }
        }
    }
}

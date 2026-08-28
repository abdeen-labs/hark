//
//  SafetySourcesView.swift
//  Hark
//
//  Critical services and Critical Alert permission.
//

import SwiftUI
import UIKit

struct SafetySourcesView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    private static let permissionAnchor = "critical-permission"

    @State private var sources: [APISafetySource] = []
    @State private var loaded = false
    @State private var errorMessage: String?
    @State private var testNotice: (kind: Notice.Kind, message: String)?
    @State private var name = ""
    @State private var imageUrl = ""
    @State private var destinationUrl = ""
    @State private var newServiceCritical = true
    @State private var editingSource: APISafetySource?
    @State private var creating = false
    @State private var requesting = false
    @State private var busySourceIDs: Set<String> = []
    @State private var testedAt: [String: Date] = [:]
    @FocusState private var nameFocused: Bool

    var body: some View {
        ScrollViewReader { proxy in
            List {
                Group {
                    bar
                        .padding(.top, 10)
                        .padding(.horizontal, Axis.gutter)
                    head
                    Hairline()
                    permissionModule
                        .id(Self.permissionAnchor)
                        .padding(.horizontal, Axis.gutter)
                        .padding(.top, 20)
                    if let notice = testNotice {
                        Notice(kind: notice.kind, message: notice.message)
                            .padding(.horizontal, Axis.gutter)
                            .padding(.top, 16)
                    }
                    if let errorMessage {
                        Notice(kind: .error, message: errorMessage)
                            .padding(.horizontal, Axis.gutter)
                            .padding(.top, 16)
                    }
                    states
                }
                .listRowInsets(EdgeInsets())
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)

                ForEach(Array(sources.enumerated()), id: \.element.id) { index, source in
                    SafetySourceRow(
                        number: index + 1,
                        source: source,
                        busy: busySourceIDs.contains(source.id),
                        sentAt: testedAt[source.id],
                        onEdit: { editingSource = source },
                        onToggle: { enabled in Task { await setCritical(source, enabled: enabled) } },
                        onTest: { Task { await sendTest(source) } }
                    )
                    .listRowInsets(EdgeInsets())
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        Button("Delete", role: .destructive) {
                            Task { await delete(source) }
                        }
                        .tint(Axis.signal)
                    }
                }

                createModule(proxy: proxy)
                    .padding(.horizontal, Axis.gutter)
                    .padding(.top, 24)
                    .padding(.bottom, 32)
                    .listRowInsets(EdgeInsets())
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .scrollDismissesKeyboard(.interactively)
            .refreshable { await reload() }
            .toolbarVisibility(.hidden, for: .navigationBar)
            .task { await reload() }
            .sheet(item: $editingSource) { source in
                CriticalServiceEditor(source: source) { updated in
                    if let index = sources.firstIndex(where: { $0.id == updated.id }) {
                        sources[index] = updated
                    }
                }
                .presentationDetents([.large])
            }
        }
    }

    // MARK: Composition

    private var bar: some View {
        HStack {
            Button("Settings") { dismiss() }
                .buttonStyle(.instrument(.ghost, arrow: .back, compact: true, fill: false))
                .padding(.leading, -10)
            Spacer()
        }
    }

    private var head: some View {
        VStack(alignment: .leading, spacing: 0) {
            Eyebrow(index: "04·A", label: "Priority")
                .padding(.top, 16)
            HStack(alignment: .lastTextBaseline, spacing: 24) {
                DisplayTitle(text: "Critical services", size: 40)
                Spacer(minLength: 0)
                Metric(
                    value: String(format: "%02d", sources.count),
                    label: sources.count == 1 ? "Service" : "Services",
                    size: 40,
                    alignment: .trailing
                )
                .animation(Axis.Motion.ease, value: sources.count)
            }
            .padding(.top, 24)
            .padding(.bottom, 18)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Axis.gutter)
    }

    private var permissionModule: some View {
        Module(index: "00", label: "Critical Alerts", variant: .signal) {
            VStack(alignment: .leading, spacing: 16) {
                if model.criticalAlertState == .granted {
                    HStack(spacing: 8) {
                        StatusLight(color: Axis.ok, size: 5)
                        Meta("Granted on this phone", color: Axis.ok)
                    }
                } else {
                    explanation
                    permissionState
                }
                if model.safetySettings?.criticalAlertsEnabled == false {
                    Notice(
                        kind: .warn,
                        message: "Critical Alerts are off for this account. These services arrive as Time Sensitive notifications."
                    )
                }
            }
        }
    }

    private var explanation: some View {
        Text("Critical services work like regular services. They can use an avatar and tap destination, and each one has its own Critical Alert switch.")
            .font(AxisType.copy(14))
            .lineSpacing(2)
            .foregroundStyle(Axis.inkMuted)
            .fixedSize(horizontal: false, vertical: true)
    }

    @ViewBuilder private var permissionState: some View {
        switch model.criticalAlertState {
        case .notRequested:
            VStack(alignment: .leading, spacing: 10) {
                Button(requesting ? "Requesting…" : "Enable Critical Alerts") {
                    guard !requesting else { return }
                    requesting = true
                    Task {
                        await model.requestCriticalAlertAuthorization()
                        requesting = false
                    }
                }
                .buttonStyle(.instrument(.primary))
                .disabled(requesting)
            }
        case .unavailable:
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusLight(color: Axis.warn, size: 5)
                Text("Critical Alerts aren't available. Critical services arrive as Time Sensitive notifications.")
                    .font(AxisType.copy(13))
                    .foregroundStyle(Axis.inkSubtle)
                    .fixedSize(horizontal: false, vertical: true)
            }
        case .notificationsDenied:
            deniedState("Notifications are off for Hark. Turn them on to receive alerts.")
        case .criticalDenied:
            deniedState("Critical Alerts are off for Hark. Critical services arrive as Time Sensitive notifications.")
        case .granted, .unknown:
            EmptyView()
        }
    }

    private func deniedState(_ message: String) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusLight(color: Axis.danger, size: 5)
                Text(message)
                    .font(AxisType.copy(13))
                    .foregroundStyle(Axis.inkSubtle)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Button("Open Settings") {
                if let url = URL(string: UIApplication.openNotificationSettingsURLString) {
                    UIApplication.shared.open(url)
                }
            }
            .buttonStyle(.instrument(.secondary, fill: false))
        }
    }

    @ViewBuilder private var states: some View {
        if sources.isEmpty {
            if loaded {
                EmptyNote(
                    text: "No critical services yet.",
                    detail: "Create one with the same defaults as a regular service."
                )
                .padding(.horizontal, Axis.gutter)
            } else {
                LoadingMark(text: "Reading critical services")
                    .padding(.horizontal, Axis.gutter)
                    .padding(.vertical, 24)
            }
        }
    }

    private func createModule(proxy: ScrollViewProxy) -> some View {
        Module(index: "01", label: "New critical service") {
            VStack(alignment: .leading, spacing: 18) {
                FieldFrame(label: "Name", focused: nameFocused) {
                    TextField("Home Assistant", text: $name)
                        .focused($nameFocused)
                        .submitLabel(.done)
                }
                FieldFrame(label: "Avatar image URL", hint: "Optional · use a public HTTPS image.") {
                    TextField("https://example.com/logo.png", text: $imageUrl)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                }
                FieldFrame(label: "Tap destination", hint: "Optional · a web URL or app deep link.") {
                    TextField("https://example.com", text: $destinationUrl)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                }
                AxisToggle(
                    "Critical delivery",
                    sub: "Falls back to Time Sensitive when Critical Alerts are off.",
                    isOn: newServiceCritical
                ) { newServiceCritical = $0 }
                Button(creating ? "Creating…" : "Create critical service") {
                    Task { await create(proxy: proxy) }
                }
                .buttonStyle(.instrument(.primary))
                .disabled(creating || !createValid)
            }
        }
    }

    private var createValid: Bool {
        !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    // MARK: Network

    private func reload() async {
        do {
            sources = try await model.client.safetySources()
            errorMessage = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
        loaded = true
        await model.refreshSafetySettings()
        await model.refreshNotificationPermission()
    }

    private func create(proxy: ScrollViewProxy) async {
        guard !creating, createValid else { return }
        creating = true
        defer { creating = false }
        do {
            let source = try await model.client.createSafetySource(
                name: name.trimmingCharacters(in: .whitespacesAndNewlines),
                imageUrl: optionalValue(imageUrl),
                url: optionalValue(destinationUrl),
                criticalEnabled: newServiceCritical
            )
            let wasEmpty = sources.isEmpty
            withAnimation(Axis.Motion.ease) { sources.append(source) }
            name = ""
            imageUrl = ""
            destinationUrl = ""
            newServiceCritical = true
            nameFocused = false
            errorMessage = nil
            if wasEmpty, model.criticalAlertState == .notRequested {
                withAnimation(Axis.Motion.ease) {
                    proxy.scrollTo(Self.permissionAnchor, anchor: .top)
                }
            }
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    private func setCritical(_ source: APISafetySource, enabled: Bool) async {
        guard !busySourceIDs.contains(source.id) else { return }
        busySourceIDs.insert(source.id)
        defer { busySourceIDs.remove(source.id) }
        guard let index = sources.firstIndex(where: { $0.id == source.id }) else { return }
        let previous = sources[index]
        var changed = previous
        changed.criticalEnabled = enabled
        sources[index] = changed
        do {
            let updated = try await model.client.updateSafetySource(changed)
            if let index = sources.firstIndex(where: { $0.id == updated.id }) {
                sources[index] = updated
            }
            errorMessage = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            if let index = sources.firstIndex(where: { $0.id == source.id }) {
                sources[index] = previous
            }
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    private func sendTest(_ source: APISafetySource) async {
        guard !busySourceIDs.contains(source.id) else { return }
        busySourceIDs.insert(source.id)
        defer { busySourceIDs.remove(source.id) }
        do {
            let event = try await model.client.sendSafetyTest(sourceId: source.id)
            testedAt[source.id] = .now
            if event.priority == "critical" {
                testNotice = (.ok, "Test sent as a Critical Alert.")
            } else {
                testNotice = (.warn, "Test sent as a Time Sensitive notification because Critical Alerts are off.")
            }
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch let error as HarkClientError {
            testNotice = (error.isRateLimited ? .warn : .error, SafetyTestFeedback.message(for: error))
        } catch {
            testNotice = (.error, (error as NSError).localizedDescription)
        }
    }

    private func delete(_ source: APISafetySource) async {
        do {
            try await model.client.deleteSafetySource(id: source.id)
            withAnimation(Axis.Motion.ease) {
                sources.removeAll { $0.id == source.id }
            }
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    private func optionalValue(_ value: String) -> String? {
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }
}

struct SafetySourceRow: View {
    let number: Int
    let source: APISafetySource
    let busy: Bool
    let sentAt: Date?
    let onEdit: () -> Void
    let onToggle: (Bool) -> Void
    let onTest: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 0) {
                LedgerIndex(number: number)
                    .padding(.top, 2)
                VStack(alignment: .leading, spacing: 10) {
                    HStack(spacing: 8) {
                        avatar
                        Text(source.name)
                            .font(AxisType.copy(15, weight: .medium))
                            .foregroundStyle(Axis.ink)
                            .lineLimit(1)
                        Spacer(minLength: 8)
                    }
                    if let destination = source.url {
                        Text(destination)
                            .font(AxisType.mono(11))
                            .foregroundStyle(Axis.inkFaint)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }

                    AxisToggle(
                        "Critical delivery",
                        sub: "Time Sensitive when switched off.",
                        compact: true,
                        busy: busy,
                        isOn: source.criticalEnabled
                    ) {
                        onToggle($0)
                    }
                    HStack(spacing: 14) {
                        Button("Manage") { onEdit() }
                            .buttonStyle(.instrument(.secondary, compact: true, fill: false))
                            .disabled(busy)
                        Button("Send test") { onTest() }
                            .buttonStyle(.instrument(.ghost, compact: true, fill: false))
                            .padding(.leading, -10)
                            .disabled(busy)
                        if let sentAt {
                            Meta("Sent \(AxisClock.short(sentAt))")
                        }
                    }
                }
            }
            .padding(.horizontal, Axis.gutter)
            .padding(.vertical, 14)
            Hairline()
        }
    }

    private var avatar: some View {
        ZStack {
            RoundedRectangle(cornerRadius: Axis.Radius.sm, style: .continuous)
                .fill(Axis.field)
            Text(String(source.name.prefix(1)).uppercased())
                .font(AxisType.meta(12))
                .foregroundStyle(Axis.inkFaint)
            if let url = source.imageUrl.flatMap(URL.init(string:)) {
                AsyncImage(url: url) { phase in
                    if case .success(let image) = phase {
                        image.resizable().scaledToFill()
                    }
                }
            }
        }
        .frame(width: 36, height: 36)
        .clipShape(RoundedRectangle(cornerRadius: Axis.Radius.sm, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: Axis.Radius.sm, style: .continuous)
                .strokeBorder(Color.primary.opacity(0.1), lineWidth: 1)
        )
    }
}

private struct CriticalServiceEditor: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    let source: APISafetySource
    let onSaved: (APISafetySource) -> Void

    @State private var name: String
    @State private var imageUrl: String
    @State private var destinationUrl: String
    @State private var criticalEnabled: Bool
    @State private var saving = false
    @State private var errorMessage: String?
    @FocusState private var focused: Field?

    private enum Field {
        case name, image, destination
    }

    init(source: APISafetySource, onSaved: @escaping (APISafetySource) -> Void) {
        self.source = source
        self.onSaved = onSaved
        _name = State(initialValue: source.name)
        _imageUrl = State(initialValue: source.imageUrl ?? "")
        _destinationUrl = State(initialValue: source.url ?? "")
        _criticalEnabled = State(initialValue: source.criticalEnabled)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    HStack {
                        Button("Close") { dismiss() }
                            .buttonStyle(.instrument(.ghost, compact: true, fill: false))
                            .padding(.leading, -10)
                        Spacer()
                    }
                    Eyebrow(index: "04·B", label: "Critical service")
                    DisplayTitle(text: "Manage service", size: 40)
                    Module(index: "01", label: "Defaults", variant: .marked) {
                        VStack(alignment: .leading, spacing: 18) {
                            FieldFrame(label: "Name", focused: focused == .name) {
                                TextField("Home Assistant", text: $name)
                                    .focused($focused, equals: .name)
                            }
                            FieldFrame(label: "Avatar image URL", hint: "Optional · use a public HTTPS image.", focused: focused == .image) {
                                TextField("https://example.com/logo.png", text: $imageUrl)
                                    .keyboardType(.URL)
                                    .textContentType(.URL)
                                    .autocorrectionDisabled()
                                    .textInputAutocapitalization(.never)
                                    .focused($focused, equals: .image)
                            }
                            FieldFrame(label: "Tap destination", hint: "Optional · a web URL or app deep link.", focused: focused == .destination) {
                                TextField("https://example.com", text: $destinationUrl)
                                    .keyboardType(.URL)
                                    .textContentType(.URL)
                                    .autocorrectionDisabled()
                                    .textInputAutocapitalization(.never)
                                    .focused($focused, equals: .destination)
                            }
                            AxisToggle(
                                "Critical delivery",
                                sub: "Falls back to Time Sensitive when switched off.",
                                busy: saving,
                                isOn: criticalEnabled
                            ) { criticalEnabled = $0 }
                            if let errorMessage {
                                Notice(kind: .error, message: errorMessage)
                            }
                            Button(saving ? "Saving…" : "Save defaults") {
                                Task { await save() }
                            }
                            .buttonStyle(.instrument(.primary))
                            .disabled(saving || name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        }
                    }
                }
                .padding(.horizontal, Axis.gutter)
                .padding(.vertical, 20)
            }
            .scrollDismissesKeyboard(.interactively)
            .toolbarVisibility(.hidden, for: .navigationBar)
        }
    }

    private func save() async {
        guard !saving else { return }
        saving = true
        defer { saving = false }
        var changed = source
        changed.name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        changed.imageUrl = optionalValue(imageUrl)
        changed.url = optionalValue(destinationUrl)
        changed.criticalEnabled = criticalEnabled
        do {
            let updated = try await model.client.updateSafetySource(changed)
            onSaved(updated)
            dismiss()
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    private func optionalValue(_ value: String) -> String? {
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }
}

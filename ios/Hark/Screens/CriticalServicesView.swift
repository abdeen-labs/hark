//
//  CriticalServicesView.swift
//  Hark
//
//  Critical services and Critical Alert permission.
//

import SwiftUI
import UIKit

struct CriticalServicesView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    private static let permissionAnchor = "critical-permission"
    private static let priorities = ["normal", "time_sensitive", "critical"]

    @State private var services: [APICriticalService] = []
    @State private var loaded = false
    @State private var errorMessage: String?
    @State private var actionNotice: (kind: Notice.Kind, message: String)?
    @State private var title = ""
    @State private var imageUrl = ""
    @State private var destinationUrl = ""
    @State private var newServicePriority = "normal"
    @State private var newServiceCritical = true
    @State private var editingService: APICriticalService?
    @State private var creating = false
    @State private var requesting = false
    @State private var busyServiceIDs: Set<String> = []
    @FocusState private var titleFocused: Bool

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
                    if let notice = actionNotice {
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

                ForEach(Array(services.enumerated()), id: \.element.id) { index, service in
                    CriticalServiceRow(
                        number: index + 1,
                        service: service,
                        busy: busyServiceIDs.contains(service.id),
                        onEdit: { editingService = service },
                        onToggle: { enabled in Task { await setCritical(service, enabled: enabled) } },
                        onRotate: { Task { await rotateWebhook(service) } }
                    )
                    .listRowInsets(EdgeInsets())
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        Button("Delete", role: .destructive) {
                            Task { await delete(service) }
                        }
                        .tint(Axis.signalDeep)
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
            .sheet(item: $editingService) { service in
                CriticalServiceEditor(service: service) { updated in
                    if let index = services.firstIndex(where: { $0.id == updated.id }) {
                        services[index] = updated
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
                    value: String(format: "%02d", services.count),
                    label: services.count == 1 ? "Service" : "Services",
                    size: 40,
                    alignment: .trailing
                )
                .animation(Axis.Motion.ease, value: services.count)
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
                if model.criticalSettings?.criticalAlertsEnabled == false {
                    Notice(
                        kind: .warn,
                        message: "Critical requests fall back to Time Sensitive while the account switch is off. Normal and Time Sensitive requests are unchanged."
                    )
                }
            }
        }
    }

    private var explanation: some View {
        Text("Critical services use the same webhook and options as regular services. They add Critical as a priority, gated by the account and service switches.")
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
        case .notificationsDenied:
            deniedState("Notifications are off for Hark. Turn them on to receive alerts.")
        case .criticalDenied:
            deniedState("Critical Alerts are off for Hark on this phone. Turn off either Critical switch to fall back to Time Sensitive.")
        case .granted, .unknown:
            EmptyView()
        }
    }

    private func deniedState(_ message: String) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusLight(color: Axis.alarm, size: 5)
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
        if services.isEmpty {
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
                FieldFrame(label: "Title", focused: titleFocused) {
                    TextField("Home Assistant", text: $title)
                        .focused($titleFocused)
                        .submitLabel(.done)
                }
                FieldFrame(label: "Default priority") {
                    Picker("Default priority", selection: $newServicePriority) {
                        ForEach(Self.priorities, id: \.self) { value in
                            Text(value.replacingOccurrences(of: "_", with: " ").capitalized).tag(value)
                        }
                    }
                    .labelsHidden()
                    .pickerStyle(.menu)
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
                    sub: "Only gates Critical. Normal and Time Sensitive are unchanged.",
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
        !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    // MARK: Network

    private func reload() async {
        do {
            services = try await model.client.criticalServices()
            errorMessage = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
        loaded = true
        await model.refreshCriticalSettings()
        await model.refreshNotificationPermission()
    }

    private func create(proxy: ScrollViewProxy) async {
        guard !creating, createValid else { return }
        creating = true
        defer { creating = false }
        do {
            let service = try await model.client.createCriticalService(
                title: title.trimmingCharacters(in: .whitespacesAndNewlines),
                imageUrl: optionalValue(imageUrl),
                url: optionalValue(destinationUrl),
                priority: newServicePriority,
                criticalEnabled: newServiceCritical
            )
            let wasEmpty = services.isEmpty
            withAnimation(Axis.Motion.ease) { services.append(service) }
            title = ""
            imageUrl = ""
            destinationUrl = ""
            newServicePriority = "normal"
            newServiceCritical = true
            titleFocused = false
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

    private func setCritical(_ service: APICriticalService, enabled: Bool) async {
        guard !busyServiceIDs.contains(service.id) else { return }
        busyServiceIDs.insert(service.id)
        defer { busyServiceIDs.remove(service.id) }
        guard let index = services.firstIndex(where: { $0.id == service.id }) else { return }
        let previous = services[index]
        var changed = previous
        changed.criticalEnabled = enabled
        services[index] = changed
        do {
            let updated = try await model.client.updateCriticalService(changed)
            if let index = services.firstIndex(where: { $0.id == updated.id }) {
                services[index] = updated
            }
            errorMessage = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            if let index = services.firstIndex(where: { $0.id == service.id }) {
                services[index] = previous
            }
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    private func rotateWebhook(_ service: APICriticalService) async {
        guard !busyServiceIDs.contains(service.id) else { return }
        busyServiceIDs.insert(service.id)
        defer { busyServiceIDs.remove(service.id) }
        do {
            let updated = try await model.client.rotateCriticalServiceWebhook(id: service.id)
            if let index = services.firstIndex(where: { $0.id == updated.id }) {
                services[index] = updated
            }
            actionNotice = (.ok, "Webhook URL rotated. The previous URL no longer works.")
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }

    private func delete(_ service: APICriticalService) async {
        do {
            try await model.client.deleteCriticalService(id: service.id)
            withAnimation(Axis.Motion.ease) {
                services.removeAll { $0.id == service.id }
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

struct CriticalServiceRow: View {
    let number: Int
    let service: APICriticalService
    let busy: Bool
    let onEdit: () -> Void
    let onToggle: (Bool) -> Void
    let onRotate: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 0) {
                LedgerIndex(number: number)
                    .padding(.top, 2)
                VStack(alignment: .leading, spacing: 10) {
                    HStack(spacing: 8) {
                        avatar
                        Text(service.title)
                            .font(AxisType.copy(15, weight: .medium))
                            .foregroundStyle(Axis.ink)
                            .lineLimit(1)
                        Spacer(minLength: 8)
                    }
                    if let destination = service.url {
                        Text(destination)
                            .font(AxisType.mono(11))
                            .foregroundStyle(Axis.inkFaint)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    HStack(spacing: 10) {
                        Meta("Default: \(service.priority.replacingOccurrences(of: "_", with: " ").capitalized)")
                        if let webhookUrl = service.webhookUrl {
                            Button("Copy webhook") {
                                UIPasteboard.general.string = webhookUrl
                            }
                            .buttonStyle(.instrument(.ghost, compact: true, fill: false))
                            .padding(.leading, -10)
                        }
                    }

                    AxisToggle(
                        "Critical delivery",
                        sub: "Only gates Critical priority.",
                        compact: true,
                        busy: busy,
                        isOn: service.criticalEnabled
                    ) {
                        onToggle($0)
                    }
                    HStack(spacing: 14) {
                        Button("Manage") { onEdit() }
                            .buttonStyle(.instrument(.secondary, compact: true, fill: false))
                            .disabled(busy)
                        Button("Rotate URL") { onRotate() }
                            .buttonStyle(.instrument(.ghost, compact: true, fill: false))
                            .padding(.leading, -10)
                            .disabled(busy)
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
            Text(String(service.title.prefix(1)).uppercased())
                .font(AxisType.meta(12))
                .foregroundStyle(Axis.inkFaint)
            if let url = service.imageUrl.flatMap(URL.init(string:)) {
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
                .strokeBorder(Axis.line, lineWidth: 1)
        )
    }
}

private struct CriticalServiceEditor: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    let service: APICriticalService
    let onSaved: (APICriticalService) -> Void

    @State private var title: String
    @State private var imageUrl: String
    @State private var destinationUrl: String
    @State private var priority: String
    @State private var criticalEnabled: Bool
    @State private var saving = false
    @State private var errorMessage: String?
    @FocusState private var focused: Field?

    private enum Field {
        case title, image, destination
    }

    init(service: APICriticalService, onSaved: @escaping (APICriticalService) -> Void) {
        self.service = service
        self.onSaved = onSaved
        _title = State(initialValue: service.title)
        _imageUrl = State(initialValue: service.imageUrl ?? "")
        _destinationUrl = State(initialValue: service.url ?? "")
        _priority = State(initialValue: service.priority)
        _criticalEnabled = State(initialValue: service.criticalEnabled)
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
                            FieldFrame(label: "Title", focused: focused == .title) {
                                TextField("Home Assistant", text: $title)
                                    .focused($focused, equals: .title)
                            }
                            FieldFrame(label: "Default priority") {
                                Picker("Default priority", selection: $priority) {
                                    Text("Normal").tag("normal")
                                    Text("Time Sensitive").tag("time_sensitive")
                                    Text("Critical").tag("critical")
                                }
                                .labelsHidden()
                                .pickerStyle(.menu)
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
                                sub: "Only gates Critical. Normal and Time Sensitive are unchanged.",
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
                            .disabled(saving || title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
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
        var changed = service
        changed.title = title.trimmingCharacters(in: .whitespacesAndNewlines)
        changed.imageUrl = optionalValue(imageUrl)
        changed.url = optionalValue(destinationUrl)
        changed.priority = priority
        changed.criticalEnabled = criticalEnabled
        do {
            let updated = try await model.client.updateCriticalService(changed)
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

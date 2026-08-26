//
//  SafetySourcesView.swift
//  Hark
//
//  Alert sources and Critical Alert permission.
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
                        onKind: { kind in Task { await setKind(source, kind: kind) } },
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
                DisplayTitle(text: "Alert sources", size: 44)
                Spacer(minLength: 0)
                Metric(
                    value: String(format: "%02d", sources.count),
                    label: sources.count == 1 ? "Source" : "Sources",
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
                        message: "Critical Alerts are off for this account. Active alerts arrive as Time Sensitive notifications."
                    )
                }
            }
        }
    }

    private var explanation: some View {
        Text("Sources start as Time Sensitive. Assign a safety alert type only when a source reports an immediate risk, then turn on Critical Alerts.")
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
                .disabled(!hasCriticalCandidate || requesting)
                if !hasCriticalCandidate {
                    Text(sources.isEmpty ? "Create a source first." : "Assign a safety alert type first.")
                        .font(AxisType.copy(12))
                        .foregroundStyle(Axis.inkFaint)
                }
            }
        case .unavailable:
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusLight(color: Axis.warn, size: 5)
                Text("Critical Alerts aren't available. Active alerts arrive as Time Sensitive notifications.")
                    .font(AxisType.copy(13))
                    .foregroundStyle(Axis.inkSubtle)
                    .fixedSize(horizontal: false, vertical: true)
            }
        case .notificationsDenied:
            deniedState("Notifications are off for Hark. Turn them on to receive alerts.")
        case .criticalDenied:
            deniedState("Critical Alerts are off for Hark. Active alerts arrive as Time Sensitive notifications.")
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
                    text: "No alert sources yet.",
                    detail: "Add a trusted app, service, or automation."
                )
                .padding(.horizontal, Axis.gutter)
            } else {
                LoadingMark(text: "Reading sources")
                    .padding(.horizontal, Axis.gutter)
                    .padding(.vertical, 24)
            }
        }
    }

    private func createModule(proxy: ScrollViewProxy) -> some View {
        Module(index: "01", label: "New source") {
            VStack(alignment: .leading, spacing: 18) {
                FieldFrame(label: "Name", focused: nameFocused) {
                    TextField("Home Assistant", text: $name)
                        .focused($nameFocused)
                        .submitLabel(.done)
                }
                Button(creating ? "Creating…" : "Create source") {
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

    private var hasCriticalCandidate: Bool {
        sources.contains { SafetyKindDisplay.allowsCritical($0.kind) }
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
                name: name.trimmingCharacters(in: .whitespacesAndNewlines)
            )
            let wasEmpty = sources.isEmpty
            withAnimation(Axis.Motion.ease) { sources.append(source) }
            name = ""
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

    private func setKind(_ source: APISafetySource, kind: String) async {
        guard !busySourceIDs.contains(source.id), source.kind != kind else { return }
        busySourceIDs.insert(source.id)
        defer { busySourceIDs.remove(source.id) }
        guard let index = sources.firstIndex(where: { $0.id == source.id }) else { return }
        let previous = sources[index]
        sources[index].kind = kind
        if !SafetyKindDisplay.allowsCritical(kind) {
            sources[index].criticalEnabled = false
        }
        do {
            let updated = try await model.client.updateSafetySource(
                id: source.id,
                kind: kind,
                criticalEnabled: SafetyKindDisplay.allowsCritical(kind) ? nil : false
            )
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

    private func setCritical(_ source: APISafetySource, enabled: Bool) async {
        guard !busySourceIDs.contains(source.id) else { return }
        guard !enabled || SafetyKindDisplay.allowsCritical(source.kind) else {
            errorMessage = "Assign a safety alert type before enabling Critical Alerts."
            return
        }
        busySourceIDs.insert(source.id)
        defer { busySourceIDs.remove(source.id) }
        guard let index = sources.firstIndex(where: { $0.id == source.id }) else { return }
        let previous = sources[index].criticalEnabled
        sources[index].criticalEnabled = enabled
        do {
            let updated = try await model.client.updateSafetySource(id: source.id, criticalEnabled: enabled)
            if let index = sources.firstIndex(where: { $0.id == updated.id }) {
                sources[index] = updated
            }
            errorMessage = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            if let index = sources.firstIndex(where: { $0.id == source.id }) {
                sources[index].criticalEnabled = previous
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
}

struct SafetySourceRow: View {
    let number: Int
    let source: APISafetySource
    let busy: Bool
    let sentAt: Date?
    let onKind: (String) -> Void
    let onToggle: (Bool) -> Void
    let onTest: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 0) {
                LedgerIndex(number: number)
                    .padding(.top, 2)
                VStack(alignment: .leading, spacing: 10) {
                    HStack(spacing: 8) {
                        Text(source.name)
                            .font(AxisType.copy(15, weight: .medium))
                            .foregroundStyle(Axis.ink)
                            .lineLimit(1)
                        Spacer(minLength: 8)
                    }
                    Picker(
                        "Alert type",
                        selection: Binding(
                            get: { source.kind },
                            set: { onKind($0) }
                        )
                    ) {
                        Text("General · Time Sensitive").tag("general")
                        ForEach(SafetyKindDisplay.all, id: \.self) { kind in
                            Text(SafetyKindDisplay.label(kind)).tag(kind)
                        }
                    }
                    .pickerStyle(.menu)
                    .tint(Axis.inkMuted)
                    .disabled(busy)

                    AxisToggle(
                        "Critical",
                        sub: source.kind == "general" ? "Choose a safety alert type first." : nil,
                        compact: true,
                        busy: busy,
                        disabled: !SafetyKindDisplay.allowsCritical(source.kind),
                        isOn: source.criticalEnabled
                    ) {
                        onToggle($0)
                    }
                    HStack(spacing: 14) {
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
}

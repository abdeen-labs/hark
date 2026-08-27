//
//  DevicesView.swift
//  Hark
//
//  Registered phones. GET /devices, DELETE /devices/{id}. A registry:
//  the count beside the title, one indexed row per handset, this one marked
//  with a signal strip.
//

import SwiftUI

struct DevicesView: View {
    @Environment(AppModel.self) private var model

    @State private var devices: [APIDevice] = []
    @State private var loaded = false
    @State private var errorMessage: String?
    @State private var confirmingSelfDelete: APIDevice?

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                head
                Hairline()
                content
            }
            .shellInsets()
            .toolbarVisibility(.hidden, for: .navigationBar)
            .task { await reload() }
            .confirmationDialog(
                "Remove this device?",
                isPresented: .init(
                    get: { confirmingSelfDelete != nil },
                    set: { if !$0 { confirmingSelfDelete = nil } }
                ),
                titleVisibility: .visible
            ) {
                Button("Remove this device", role: .destructive) {
                    if let device = confirmingSelfDelete {
                        Task { await delete(device) }
                    }
                }
                Button("Keep", role: .cancel) {}
            } message: {
                Text("Pushes stop until the app registers again on next launch.")
            }
        }
    }

    private var head: some View {
        VStack(alignment: .leading, spacing: 0) {
            Eyebrow(index: "03", label: "Registry")
            .padding(.top, 20)
            HStack(alignment: .lastTextBaseline, spacing: 24) {
                DisplayTitle(text: "Devices", size: 56)
                Spacer(minLength: 0)
                Metric(
                    value: String(format: "%02d", devices.count),
                    of: loaded ? String(devices.filter(\.active).count) : nil,
                    label: loaded ? "Registered / Active" : "Registered",
                    size: 44,
                    alignment: .trailing
                )
                .animation(Axis.Motion.ease, value: devices.count)
            }
            .padding(.top, 28)
            .padding(.bottom, 18)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Axis.gutter)
    }

    @ViewBuilder private var content: some View {
        if devices.isEmpty {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    if let errorMessage {
                        Notice(kind: .error, message: errorMessage)
                            .padding(.top, 16)
                    }
                    if loaded {
                        EmptyNote(
                            text: "No device is registered.",
                            detail: "Launch Hark on a phone to register it."
                        )
                    } else {
                        LoadingMark(text: "Reading the registry")
                            .padding(.vertical, 24)
                    }
                }
                .padding(.horizontal, Axis.gutter)
            }
            .refreshable { await reload() }
        } else {
            List {
                ForEach(Array(devices.enumerated()), id: \.element.id) { index, device in
                    DeviceRow(number: index + 1, device: device, isThisDevice: device.id == model.deviceID)
                        .listRowInsets(EdgeInsets())
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                            Button("Remove", role: .destructive) {
                                if device.id == model.deviceID {
                                    confirmingSelfDelete = device
                                } else {
                                    Task { await delete(device) }
                                }
                            }
                            .tint(Axis.signal)
                        }
                }
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .refreshable { await reload() }
        }
    }

    private func reload() async {
        do {
            devices = try await model.client.devices()
            errorMessage = nil
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
        loaded = true
    }

    private func delete(_ device: APIDevice) async {
        do {
            try await model.client.deleteDevice(id: device.id)
            devices.removeAll { $0.id == device.id }
        } catch let error as HarkClientError where error.isUnauthorized {
            model.handleUnauthorized()
        } catch {
            errorMessage = (error as? HarkClientError)?.errorDescription
                ?? (error as NSError).localizedDescription
        }
    }
}

struct DeviceRow: View {
    let number: Int
    let device: APIDevice
    let isThisDevice: Bool

    var body: some View {
        VStack(spacing: 0) {
            HStack(alignment: .top, spacing: 0) {
                LedgerIndex(number: number)
                    .padding(.top, 2)
                VStack(alignment: .leading, spacing: 8) {
                    HStack(spacing: 8) {
                        Text(device.name ?? "Unnamed")
                            .font(AxisType.copy(15, weight: .medium))
                            .foregroundStyle(device.name == nil ? Axis.inkFaint : Axis.ink)
                            .lineLimit(1)
                        if isThisDevice {
                            Tag("This device", tone: .signal)
                        }
                        Spacer(minLength: 0)
                    }
                    Text(device.id)
                        .font(AxisType.mono(11))
                        .foregroundStyle(Axis.inkFaint)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    FlowRow {
                        StateTag(state: device.active ? "active" : "retired")
                        if device.liveActivityCapable {
                            Tag("Ready", tone: .ok, light: true)
                            if device.liveActivityInteractionVersion == nil {
                                Tag("No buttons")
                            }
                        } else {
                            Tag("No push-to-start", tone: .muted)
                        }
                        if device.interactionSchemaVersion != nil {
                            Tag("Answers")
                        }
                    }
                    HStack(spacing: 14) {
                        Meta("Seen \(AxisClock.age(device.lastSeenAt))")
                        if let environment = device.pushToStartEnvironment, !environment.isEmpty {
                            Meta("APNs \(environment)")
                        }
                        Meta("Since \(device.createdAt.formatted(.dateTime.day(.twoDigits).month(.abbreviated)))")
                    }
                    .padding(.top, 2)
                }
            }
            .padding(.horizontal, Axis.gutter)
            .padding(.vertical, 14)
            .overlay(alignment: .leading) {
                if isThisDevice {
                    Rectangle().fill(Axis.signal).frame(width: 4)
                }
            }
            Hairline()
        }
    }
}

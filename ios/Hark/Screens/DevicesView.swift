//
//  DevicesView.swift
//  Hark
//
//  Registered phones. GET /v1/devices, DELETE /v1/devices/{id}.
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
            Group {
                if devices.isEmpty {
                    ScrollView {
                        if let errorMessage {
                            Text(errorMessage)
                                .font(.footnote)
                                .foregroundStyle(Axis.accent)
                                .padding()
                        }
                        if loaded {
                            AxisEmptyState(
                                icon: "iphone.slash",
                                title: "No devices",
                                detail: "Devices appear here once they register for pushes."
                            )
                        } else {
                            ProgressView().tint(Axis.accent).padding(.top, 48)
                        }
                    }
                    .refreshable { await reload() }
                } else {
                    List {
                        ForEach(devices) { device in
                            DeviceRow(device: device, isThisDevice: device.id == model.deviceID)
                                .listRowBackground(Axis.plate)
                                .listRowSeparatorTint(Axis.stroke)
                                .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                                    Button("Remove", role: .destructive) {
                                        if device.id == model.deviceID {
                                            confirmingSelfDelete = device
                                        } else {
                                            Task { await delete(device) }
                                        }
                                    }
                                }
                        }
                    }
                    .listStyle(.insetGrouped)
                    .scrollContentBackground(.hidden)
                    .refreshable { await reload() }
                }
            }
            .background(Axis.bg)
            .navigationTitle("Devices")
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
    let device: APIDevice
    let isThisDevice: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Image(systemName: "iphone")
                    .font(.caption)
                    .foregroundStyle(device.active ? Axis.jade : Axis.textTertiary)
                Text(device.name ?? "Unnamed device")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(Axis.textPrimary)
                if isThisDevice {
                    AxisChip(text: "This device", tint: Axis.accent)
                }
                Spacer()
            }
            HStack(spacing: 6) {
                if !device.active {
                    AxisChip(text: "Inactive", tint: Axis.textTertiary)
                }
                if device.liveActivityCapable {
                    AxisChip(text: "Live Activities", tint: Axis.jade)
                }
                if device.interactionSchemaVersion != nil {
                    AxisChip(text: "Answers", tint: Axis.textSecondary)
                }
                Spacer()
                Text("Seen \(device.lastSeenAt.formatted(.relative(presentation: .named)))")
                    .font(.caption2)
                    .monospacedDigit()
                    .foregroundStyle(Axis.textTertiary)
            }
        }
        .padding(.vertical, 3)
    }
}

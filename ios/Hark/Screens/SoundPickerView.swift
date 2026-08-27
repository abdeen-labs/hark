//
//  SoundPickerView.swift
//  Hark
//

import AVFoundation
import SwiftUI

struct SoundPickerView: View {
    @Environment(\.dismiss) private var dismiss

    @State private var selectedID = HarkSoundCatalog.selectedToneID
    @State private var player: AVAudioPlayer?
    @State private var teardown: Task<Void, Never>?

    var body: some View {
        ScrollView {
            VStack(spacing: 0) {
                bar
                    .padding(.top, 10)
                    .padding(.horizontal, Axis.gutter)
                head
                Hairline()
                ledger
                Text("Critical Alerts use their own sound.")
                    .font(AxisType.copy(12))
                    .foregroundStyle(Axis.inkFaint)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, Axis.gutter)
                    .padding(.top, 16)
            }
            .padding(.bottom, 32)
        }
        .toolbarVisibility(.hidden, for: .navigationBar)
        .onDisappear { stopPreview() }
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
            Eyebrow(index: "04·B", label: "Sound")
                .padding(.top, 16)
            HStack(alignment: .lastTextBaseline, spacing: 24) {
                DisplayTitle(text: "Tone", size: 44)
                Spacer(minLength: 0)
                Metric(
                    value: String(format: "%02d", HarkSoundCatalog.tones.count),
                    label: "Tones",
                    size: 40,
                    alignment: .trailing
                )
            }
            .padding(.top, 24)
            .padding(.bottom, 18)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Axis.gutter)
    }

    private var ledger: some View {
        VStack(spacing: 0) {
            row(number: 0, name: "Default", selected: selectedID == nil) {
                choose(nil)
            }
            ForEach(Array(HarkSoundCatalog.tones.enumerated()), id: \.element.id) { index, tone in
                row(number: index + 1, name: tone.name, selected: selectedID == tone.id) {
                    choose(tone)
                }
            }
        }
    }

    private func row(number: Int, name: String, selected: Bool, action: @escaping () -> Void) -> some View {
        VStack(spacing: 0) {
            Button(action: action) {
                HStack(spacing: 0) {
                    LedgerIndex(number: number)
                    Text(name)
                        .font(AxisType.copy(15, weight: .medium))
                        .foregroundStyle(Axis.ink)
                        .lineLimit(1)
                    Spacer(minLength: 8)
                    if selected {
                        HStack(spacing: 8) {
                            StatusLight(color: Axis.signalText, size: 6)
                            Meta("Selected", color: Axis.signalText)
                        }
                    }
                }
                .padding(.horizontal, Axis.gutter)
                .padding(.vertical, 16)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(name)
            .accessibilityAddTraits(selected ? [.isSelected] : [])
            Hairline()
        }
        .animation(Axis.Motion.quick, value: selected)
    }

    // MARK: Selection and preview

    private func choose(_ tone: HarkSoundCatalog.Tone?) {
        HarkSoundCatalog.select(tone)
        withAnimation(Axis.Motion.quick) { selectedID = tone?.id }
        if let tone {
            preview(tone)
        } else {
            stopPreview()
        }
    }

    private func preview(_ tone: HarkSoundCatalog.Tone) {
        teardown?.cancel()
        teardown = nil
        guard let url = Bundle.main.url(forResource: tone.id, withExtension: "caf") else { return }
        do {
            try AVAudioSession.sharedInstance().setCategory(.playback)
            try AVAudioSession.sharedInstance().setActive(true)
            let player = try AVAudioPlayer(contentsOf: url)
            player.play()
            self.player = player
            teardown = Task {
                try? await Task.sleep(for: .seconds(player.duration + 0.2))
                guard !Task.isCancelled else { return }
                releaseSession()
            }
        } catch {
            releaseSession()
        }
    }

    private func stopPreview() {
        teardown?.cancel()
        teardown = nil
        guard player != nil else { return }
        releaseSession()
    }

    private func releaseSession() {
        player?.stop()
        player = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }
}

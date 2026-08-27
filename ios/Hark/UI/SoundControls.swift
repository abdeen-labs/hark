//
//  SoundControls.swift
//  Hark
//

import SwiftUI

struct SoundModule<Route: Hashable>: View {
    let index: String
    let route: Route

    @State private var toneName = HarkSoundCatalog.selectedTone?.name ?? "Default"

    var body: some View {
        Module(index: index, label: "Sound", flush: true) {
            NavigationLink(value: route) {
                HStack(spacing: 16) {
                    Meta("Tone")
                        .frame(width: 100, alignment: .leading)
                    Text(toneName)
                        .font(AxisType.copy(14))
                        .foregroundStyle(Axis.inkMuted)
                    Spacer(minLength: 8)
                    Text("→")
                        .axisControl(12)
                        .foregroundStyle(Axis.inkSubtle)
                }
                .padding(.vertical, 9)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 16)
            .padding(.vertical, 4)
        }
        .onAppear {
            toneName = HarkSoundCatalog.selectedTone?.name ?? "Default"
        }
    }
}

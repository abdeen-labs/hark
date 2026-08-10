//
//  HarkApp.swift
//  Hark
//
//  Entry point. Dark-only by design — Axis has no light theme.
//

import SwiftUI

@main
struct HarkApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @Environment(\.scenePhase) private var scenePhase

    private let model = AppModel.shared

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .preferredColorScheme(.dark)
                .tint(Axis.accent)
                .background(Axis.bg)
                .task { await model.bootstrap() }
                .onOpenURL { url in
                    model.handleDeepLink(url)
                }
                .onChange(of: scenePhase) { _, newPhase in
                    if newPhase == .active {
                        Task { await model.didBecomeActive() }
                    }
                }
        }
    }
}

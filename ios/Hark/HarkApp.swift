//
//  HarkApp.swift
//  Hark
//
//  Entry point. The appearance setting decides the colour scheme; the app
//  lock covers everything below it, so it sits at this level rather than in
//  any screen.
//

import SwiftUI

@main
struct HarkApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @Environment(\.scenePhase) private var scenePhase
    @AppStorage(Appearance.storageKey) private var appearance = Appearance.system.rawValue

    private let model = AppModel.shared
    private let appLock = AppLock()

    var body: some Scene {
        WindowGroup {
            ZStack {
                RootView()
                if appLock.showsShield {
                    PrivacyShieldView()
                }
                if appLock.isLocked {
                    LockScreenView()
                }
            }
            .environment(model)
            .environment(appLock)
            .preferredColorScheme((Appearance(rawValue: appearance) ?? .system).colorScheme)
            .tint(Axis.signalText)
            .background(Axis.paper)
            .task { await model.bootstrap() }
            .onOpenURL { url in
                model.handleDeepLink(url)
            }
            .onChange(of: scenePhase) { _, newPhase in
                appLock.handleScenePhase(newPhase)
                if newPhase == .active {
                    Task { await model.didBecomeActive() }
                }
            }
        }
    }
}

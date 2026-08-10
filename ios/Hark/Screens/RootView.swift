//
//  RootView.swift
//  Hark
//
//  Switches between the loading veil, sign-in, and the app proper.
//

import SwiftUI

struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ZStack {
            Axis.bg.ignoresSafeArea()
            switch model.phase {
            case .loading:
                ProgressView()
                    .tint(Axis.accent)
            case .signedOut:
                SignInView()
            case .signedIn:
                RootTabView()
            }
        }
    }
}

struct RootTabView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        TabView(selection: $model.selectedTab) {
            InboxView()
                .tabItem { Label("Inbox", systemImage: "tray") }
                .tag(AppModel.Tab.inbox)
            HistoryView()
                .tabItem { Label("History", systemImage: "clock") }
                .tag(AppModel.Tab.history)
            DevicesView()
                .tabItem { Label("Devices", systemImage: "iphone") }
                .tag(AppModel.Tab.devices)
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape") }
                .tag(AppModel.Tab.settings)
        }
        .toolbarBackground(Axis.bg, for: .tabBar)
        .toolbarBackground(.visible, for: .tabBar)
    }
}

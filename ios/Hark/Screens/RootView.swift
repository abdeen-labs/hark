//
//  RootView.swift
//  Hark
//
//  Switches between the loading veil, the sign-in gate, and the app proper.
//  The paper and its dot matrix sit here, under every phase.
//

import SwiftUI

struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ZStack {
            Axis.paper.ignoresSafeArea()
            DotMatrix().ignoresSafeArea()
            switch model.phase {
            case .loading:
                LoadingMark(text: "Validating session")
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomLeading)
                    .padding(.horizontal, Axis.gutter)
                    .padding(.bottom, 32)
            case .signedOut:
                SignInView()
            case .signedIn:
                RootTabView()
            }
        }
    }
}

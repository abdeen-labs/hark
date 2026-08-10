//
//  HarkWidgetsBundle.swift
//  HarkWidgets
//
//  The widget extension exists for one thing: Hark's Live Activities.
//

import SwiftUI
import WidgetKit

@main
struct HarkWidgetsBundle: WidgetBundle {
    var body: some Widget {
        HarkLiveActivity()
    }
}

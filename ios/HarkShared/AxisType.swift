//
//  AxisType.swift
//  Hark
//
//  The type scale. Four registers kept far apart — 10 pt mono metadata,
//  15 pt copy, 40–110 pt data, 56–150 pt display — and most of the hierarchy
//  is the jump between them. SF Pro carries the sans registers, SF Mono the
//  technical ones. Tracking is in em and resolved against the size here so
//  no view has to remember the ratio.
//

import SwiftUI

nonisolated enum AxisType {
    // MARK: Sans

    /// The page title register.
    static func display(_ size: CGFloat = 64) -> Font {
        .system(size: size, weight: .semibold)
    }

    /// The section register: a prompt, a question, a word that leads a panel.
    static func headline(_ size: CGFloat = 32) -> Font {
        .system(size: size, weight: .semibold)
    }

    /// Interface headings.
    static func ui(_ size: CGFloat = 22) -> Font {
        .system(size: size, weight: .semibold)
    }

    /// A number at architectural scale.
    static func metric(_ size: CGFloat = 48) -> Font {
        .system(size: size, weight: .semibold)
    }

    /// Interface copy.
    static func copy(_ size: CGFloat = 15, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight)
    }

    /// Control labels.
    static func control(_ size: CGFloat = 12) -> Font {
        .system(size: size, weight: .semibold)
    }

    // MARK: Mono

    /// Metadata: labels, indexes, timestamps, states. Uppercase by use.
    static func meta(_ size: CGFloat = 10) -> Font {
        .system(size: size, weight: .medium, design: .monospaced)
    }

    /// Technical values: identifiers, URLs, measurements.
    static func mono(_ size: CGFloat = 12, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight, design: .monospaced)
    }

    // MARK: Tracking, in em

    static let displayTracking: CGFloat = -0.045
    static let headlineTracking: CGFloat = -0.035
    static let uiTracking: CGFloat = -0.02
    static let metricTracking: CGFloat = -0.045
    static let metaTracking: CGFloat = 0.1
    static let controlTracking: CGFloat = 0.07
    static let wordmarkTracking: CGFloat = 0.22

    static func tracking(_ em: CGFloat, at size: CGFloat) -> CGFloat {
        em * size
    }
}

extension Text {
    /// Display register: tight, semibold, at the given size.
    func axisDisplay(_ size: CGFloat = 64) -> Text {
        font(AxisType.display(size))
            .tracking(AxisType.tracking(AxisType.displayTracking, at: size))
    }

    func axisHeadline(_ size: CGFloat = 32) -> Text {
        font(AxisType.headline(size))
            .tracking(AxisType.tracking(AxisType.headlineTracking, at: size))
    }

    func axisUI(_ size: CGFloat = 22) -> Text {
        font(AxisType.ui(size))
            .tracking(AxisType.tracking(AxisType.uiTracking, at: size))
    }

    /// A number at architectural scale, tabular so columns of them align.
    func axisMetric(_ size: CGFloat = 48) -> Text {
        font(AxisType.metric(size))
            .monospacedDigit()
            .tracking(AxisType.tracking(AxisType.metricTracking, at: size))
    }

    func axisMeta(_ size: CGFloat = 10) -> Text {
        font(AxisType.meta(size))
            .tracking(AxisType.tracking(AxisType.metaTracking, at: size))
    }

    func axisControl(_ size: CGFloat = 12) -> Text {
        font(AxisType.control(size))
            .tracking(AxisType.tracking(AxisType.controlTracking, at: size))
    }
}

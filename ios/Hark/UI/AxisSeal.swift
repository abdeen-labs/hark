//
//  AxisSeal.swift
//  Hark
//
//  The Abdeen Labs Key and the lockup it leads. A square plate with a 26%
//  top-left chamfer, a hairline that scales with the plate and floors at one
//  point, and the mark set live in Aref Ruqaa at 0.36 of the plate, lifted
//  0.168em to its optical centre. The wordmark is Geist Mono 500 at 0.22em.
//

import CoreText
import SwiftUI
import UIKit

enum BrandFace {
    static func mark(_ size: CGFloat) -> Font {
        .custom("ArefRuqaa-Bold", fixedSize: size)
    }

    static func wordmark(_ size: CGFloat) -> Font {
        let variation = UIFontDescriptor.AttributeName(rawValue: kCTFontVariationAttribute as String)
        let descriptor = UIFontDescriptor(fontAttributes: [
            .name: "GeistMono-Regular",
            variation: [2_003_265_652: 500],
        ])
        return Font(UIFont(descriptor: descriptor, size: size))
    }
}

/// The Key's plate: a square cut at 45° across the top-left corner.
struct KeyPlate: Shape {
    static let cut: CGFloat = 0.26

    func path(in rect: CGRect) -> Path {
        let cut = min(rect.width, rect.height) * Self.cut
        var path = Path()
        path.move(to: CGPoint(x: rect.minX + cut, y: rect.minY))
        path.addLine(to: CGPoint(x: rect.maxX, y: rect.minY))
        path.addLine(to: CGPoint(x: rect.maxX, y: rect.maxY))
        path.addLine(to: CGPoint(x: rect.minX, y: rect.maxY))
        path.addLine(to: CGPoint(x: rect.minX, y: rect.minY + cut))
        path.closeSubpath()
        return path
    }
}

/// The standard seal. The field follows the ground, the line is the
/// identity scarlet, and the mark takes the ground's primary ink. 16 pt is
/// the floor.
struct KeySeal: View {
    var size: CGFloat = 40

    static let markScale: CGFloat = 0.36
    static let inkHeight: CGFloat = 0.883
    static let opticalShift: CGFloat = 0.168

    private var line: CGFloat { max(1, size / 40) }
    private var fontSize: CGFloat { size * Self.markScale / Self.inkHeight }

    var body: some View {
        ZStack {
            KeyPlate().fill(Axis.surface)
            KeyPlate().stroke(Axis.signal, lineWidth: line * 2)
            Text("عابدين")
                .font(BrandFace.mark(fontSize))
                .foregroundStyle(Axis.ink)
                .fixedSize()
                .offset(y: -fontSize * Self.opticalShift)
        }
        .frame(width: size, height: size)
        .clipShape(KeyPlate())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Abdeen Labs")
    }
}

/// `ABDEEN LABS` in Geist Mono 500, uppercase, tracked 0.22em, 11 pt or
/// larger. It takes its ink from the context.
struct Wordmark: View {
    var size: CGFloat = 11
    var color: Color = Axis.inkFaint

    var body: some View {
        Text("ABDEEN LABS")
            .font(BrandFace.wordmark(size))
            .tracking(AxisType.tracking(AxisType.wordmarkTracking, at: size))
            .foregroundStyle(color)
            .lineLimit(1)
            .fixedSize()
    }
}

/// Key · hairline divider · wordmark on one row.
struct Lockup: View {
    var sealSize: CGFloat = 24
    var color: Color = Axis.inkFaint

    var body: some View {
        HStack(spacing: 12) {
            KeySeal(size: sealSize)
            Hairline(vertical: true)
                .frame(height: sealSize)
            Wordmark(color: color)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Abdeen Labs")
    }
}

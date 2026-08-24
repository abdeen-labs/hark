# idiomorph

The dashboard includes an unmodified copy of
[idiomorph](https://github.com/bigskysoftware/idiomorph) at
`internal/dashboard/assets/idiomorph.min.js`. It is compiled into the binary
with the first-party assets. No package manager or frontend build step is
required. The dashboard uses it to update the overview without replacing
unchanged DOM nodes.

The exact package, version, tarball URL, and SHA-256 digests are recorded in
[`internal/dashboard/assets/idiomorph.provenance.json`](../../internal/dashboard/assets/idiomorph.provenance.json).
The vendoring script reads these pinned values, and a Go test verifies that the
embedded file matches the recorded digest.

`LICENSE` in this directory is `package/LICENSE` from the pinned npm tarball,
byte-for-byte. idiomorph is published under the Zero-Clause BSD license
(`0BSD`).

## Verifying

```sh
sh scripts/vendor-assets.sh --verify
```

This command works offline and verifies each vendored asset and license against
the digests in its provenance file.

## Upgrading

1. Update every field in `idiomorph.provenance.json` for the new version —
   tarball URL, registry integrity/shasum, and the artifact and license
   SHA-256s.
2. Run `sh scripts/vendor-assets.sh --refresh` to fetch the pinned tarballs and
   install the verified files. Refresh checks each archive's SHA-512 integrity
   before extraction, then checks the asset and license SHA-256s before either
   repository file is replaced.
3. Update the expected values in `internal/dashboard/assets_test.go`.
4. Commit the provenance, asset, license, and test changes together.

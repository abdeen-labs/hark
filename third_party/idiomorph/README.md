# idiomorph

The dashboard vendors [idiomorph](https://github.com/bigskysoftware/idiomorph)
as a single unmodified file, `internal/dashboard/assets/idiomorph.min.js`,
compiled into the binary alongside the first-party assets — there is no
package manager and no frontend build step. It is what the overview's poll
uses to update the page in place: the fresh fragment is morphed into the
existing DOM, so rows that did not change keep their nodes.

The exact package, version, tarball URL, and SHA-256 digests are recorded in
[`internal/dashboard/assets/idiomorph.provenance.json`](../../internal/dashboard/assets/idiomorph.provenance.json).
That file is canonical: the vendoring script reads its pins, and a Go test
binds the embedded bytes to the recorded digest.

`LICENSE` in this directory is `package/LICENSE` from the pinned npm tarball,
byte-for-byte. idiomorph is published under the Zero-Clause BSD license
(`0BSD`).

## Verifying

```sh
sh scripts/vendor-assets.sh --verify
```

Offline: checks every vendored asset and license against the digests in its
provenance file.

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

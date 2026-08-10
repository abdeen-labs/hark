# htmx

The dashboard vendors [htmx](https://htmx.org) as a single unmodified file,
`internal/dashboard/assets/htmx.min.js`, compiled into the binary alongside the
first-party assets — there is no package manager and no frontend build step.

The exact package, version, tarball URL, and SHA-256 digests are recorded in
[`internal/dashboard/assets/htmx.provenance.json`](../../internal/dashboard/assets/htmx.provenance.json).
That file is canonical: the vendoring script reads its pins, and a Go test
binds the embedded bytes to the recorded digest.

`LICENSE` in this directory is `package/LICENSE` from the pinned npm tarball,
byte-for-byte. htmx is published under the Zero-Clause BSD license (`0BSD`).

## Verifying

```sh
sh scripts/vendor-htmx.sh --verify
```

Offline: checks the installed asset and license against the digests in the
provenance file.

## Upgrading

1. Update every field in `htmx.provenance.json` for the new version — tarball
   URL, registry integrity/shasum, and the artifact and license SHA-256s.
2. Run `sh scripts/vendor-htmx.sh --refresh` to fetch the pinned tarball and
   install the verified files.
3. Update the expected values in `internal/dashboard/assets_test.go`.
4. Commit the provenance, asset, license, and test changes together.

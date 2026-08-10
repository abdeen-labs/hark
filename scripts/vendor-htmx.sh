#!/bin/sh
# vendor-htmx.sh verifies or refreshes the vendored htmx asset. The pins —
# version, tarball URL, artifact paths, and SHA-256 digests — all come from
# internal/dashboard/assets/htmx.provenance.json, so the script cannot drift
# from the recorded provenance.
#
#   sh scripts/vendor-htmx.sh --verify    offline check of the installed files
#   sh scripts/vendor-htmx.sh --refresh   re-fetch the pinned tarball, verify,
#                                         and reinstall the asset and license
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
provenance=$root/internal/dashboard/assets/htmx.provenance.json

die() {
	echo "vendor-htmx: $*" >&2
	exit 1
}

[ -f "$provenance" ] || die "missing $provenance"

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "need sha256sum or shasum on PATH"
fi

# field extracts one required string value from the provenance JSON. It is a
# deliberately strict line matcher, not a JSON parser: the file must hold
# exactly one `"key": "value"` line for the key, with a non-empty value —
# anything else is an error. Nothing is ever sourced or eval'd.
field() {
	value=$(sed -n 's/^[[:space:]]*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)"[[:space:]]*,\{0,1\}[[:space:]]*$/\1/p' "$provenance")
	[ -n "$value" ] || die "provenance field \"$1\" is missing or empty"
	[ "$(printf '%s\n' "$value" | wc -l)" -eq 1 ] || die "provenance field \"$1\" appears more than once"
	printf '%s\n' "$value"
}

package=$(field package)
version=$(field version)
tarball_url=$(field tarball_url)
artifact_path=$(field artifact_path)
installed_path=$(field installed_path)
artifact_sha256=$(field artifact_sha256)
license=$(field license)
license_artifact_path=$(field license_artifact_path)
license_installed_path=$(field license_installed_path)
license_sha256=$(field license_sha256)

# check fails unless the file exists and has the expected SHA-256.
check() {
	[ -f "$2" ] || die "$1: missing $2"
	got=$(sha256 "$2")
	[ "$got" = "$3" ] || die "$1: sha256 mismatch for $2: expected $3, got $got"
}

verify() {
	check "asset" "$root/$installed_path" "$artifact_sha256"
	check "license" "$root/$license_installed_path" "$license_sha256"
	echo "vendor-htmx: OK $package@$version ($license license), $installed_path sha256=$artifact_sha256"
}

refresh() {
	command -v curl >/dev/null 2>&1 || die "refresh needs curl"
	command -v tar >/dev/null 2>&1 || die "refresh needs tar"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	echo "vendor-htmx: fetching $tarball_url"
	curl -fsSL -o "$tmp/package.tgz" "$tarball_url"
	tar -xzf "$tmp/package.tgz" -C "$tmp" "$artifact_path" "$license_artifact_path"

	# Both digests are verified before either repository file is touched, so a
	# failed download leaves the working tree exactly as it was.
	check "downloaded asset" "$tmp/$artifact_path" "$artifact_sha256"
	check "downloaded license" "$tmp/$license_artifact_path" "$license_sha256"

	cp "$tmp/$artifact_path" "$root/$installed_path"
	cp "$tmp/$license_artifact_path" "$root/$license_installed_path"
	echo "vendor-htmx: refreshed $package@$version into $installed_path and $license_installed_path"
}

case ${1-} in
--verify) verify ;;
--refresh) refresh ;;
*)
	echo "usage: sh scripts/vendor-htmx.sh --verify|--refresh" >&2
	exit 2
	;;
esac

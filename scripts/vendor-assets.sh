#!/bin/sh
# vendor-assets.sh verifies or refreshes the dashboard's vendored assets. Each
# one is described by internal/dashboard/assets/<name>.provenance.json, and the
# script reads the version, tarball URL, artifact paths, and SHA-256 digests
# directly from that file.
#
#   sh scripts/vendor-assets.sh --verify    offline check of every installed file
#   sh scripts/vendor-assets.sh --refresh   re-fetch each pinned tarball, verify,
#                                           and reinstall the asset and license
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
assets=$root/internal/dashboard/assets

die() {
	echo "vendor-assets: $*" >&2
	exit 1
}

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "need sha256sum or shasum on PATH"
fi

# field extracts one required string value from the current provenance file.
# It is a strict line matcher, not a JSON parser: the file must
# hold exactly one `"key": "value"` line for the key, with a non-empty value —
# anything else is an error. Nothing is ever sourced or eval'd.
field() {
	value=$(sed -n 's/^[[:space:]]*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)"[[:space:]]*,\{0,1\}[[:space:]]*$/\1/p' "$provenance")
	[ -n "$value" ] || die "$provenance: field \"$1\" is missing or empty"
	[ "$(printf '%s\n' "$value" | wc -l)" -eq 1 ] || die "$provenance: field \"$1\" appears more than once"
	printf '%s\n' "$value"
}

# load reads every pin for one asset.
load() {
	provenance=$1
	package=$(field package)
	version=$(field version)
	tarball_url=$(field tarball_url)
	tarball_integrity=$(field tarball_integrity)
	artifact_path=$(field artifact_path)
	installed_path=$(field installed_path)
	artifact_sha256=$(field artifact_sha256)
	license=$(field license)
	license_artifact_path=$(field license_artifact_path)
	license_installed_path=$(field license_installed_path)
	license_sha256=$(field license_sha256)
}

# check fails unless the file exists and has the expected SHA-256.
check() {
	[ -f "$2" ] || die "$1: missing $2"
	got=$(sha256 "$2")
	[ "$got" = "$3" ] || die "$1: sha256 mismatch for $2: expected $3, got $got"
}

# check_tarball authenticates the archive before tar is allowed to parse it.
# npm integrity values are an algorithm name plus a base64-encoded digest; the
# vendor records pin SHA-512 rather than relying on the
# registry's older SHA-1 shasum.
check_tarball() {
	case $tarball_integrity in
	sha512-?*) expected=${tarball_integrity#sha512-} ;;
	*) die "$package: tarball_integrity must be a non-empty sha512 value" ;;
	esac
	got=$(openssl dgst -sha512 -binary "$1" | base64 | tr -d '\r\n')
	[ "$got" = "$expected" ] || die "$package: tarball: sha512 integrity mismatch"
}

verify() {
	check "$package asset" "$root/$installed_path" "$artifact_sha256"
	check "$package license" "$root/$license_installed_path" "$license_sha256"
	echo "vendor-assets: OK $package@$version ($license license), $installed_path sha256=$artifact_sha256"
}

refresh() {
	dir=$tmp/$(basename "$provenance" .provenance.json)
	mkdir "$dir"

	echo "vendor-assets: fetching $tarball_url"
	curl -fsSL -o "$dir/package.tgz" "$tarball_url"
	check_tarball "$dir/package.tgz"
	tar -xzf "$dir/package.tgz" -C "$dir" "$artifact_path" "$license_artifact_path"

	# Both digests are verified before either repository file is touched, so a
	# failed download leaves the working tree exactly as it was.
	check "$package downloaded asset" "$dir/$artifact_path" "$artifact_sha256"
	check "$package downloaded license" "$dir/$license_artifact_path" "$license_sha256"

	cp "$dir/$artifact_path" "$root/$installed_path"
	cp "$dir/$license_artifact_path" "$root/$license_installed_path"
	echo "vendor-assets: refreshed $package@$version into $installed_path and $license_installed_path"
}

mode=${1-}
case $mode in
--verify) ;;
--refresh)
	for tool in curl tar openssl base64; do
		command -v "$tool" >/dev/null 2>&1 || die "refresh needs $tool"
	done
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM
	;;
*)
	echo "usage: sh scripts/vendor-assets.sh --verify|--refresh" >&2
	exit 2
	;;
esac

for provenance in "$assets"/*.provenance.json; do
	[ -f "$provenance" ] || die "no provenance files under $assets"
	load "$provenance"
	case $mode in
	--verify) verify ;;
	--refresh) refresh ;;
	esac
done

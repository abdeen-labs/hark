package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// htmxProvenance mirrors the string fields of assets/htmx.provenance.json,
// the canonical record of where the vendored htmx asset came from. The same
// file drives scripts/vendor-htmx.sh, so the pins cannot drift between the
// script, the test, and the embedded bytes.
type htmxProvenance struct {
	Package              string `json:"package"`
	Version              string `json:"version"`
	Registry             string `json:"registry"`
	TarballURL           string `json:"tarball_url"`
	ArtifactPath         string `json:"artifact_path"`
	InstalledPath        string `json:"installed_path"`
	ArtifactSHA256       string `json:"artifact_sha256"`
	License              string `json:"license"`
	LicenseInstalledPath string `json:"license_installed_path"`
	LicenseSHA256        string `json:"license_sha256"`
}

// TestHTMXProvenanceMatchesEmbeddedAsset binds the provenance record to the
// bytes actually compiled into the binary: the metadata names the expected
// package, and the embedded htmx.min.js hashes to exactly the recorded digest.
func TestHTMXProvenanceMatchesEmbeddedAsset(t *testing.T) {
	raw, err := assetFS.ReadFile("assets/htmx.provenance.json")
	if err != nil {
		t.Fatalf("read embedded provenance: %v", err)
	}

	var p htmxProvenance
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode provenance: %v", err)
	}

	want := htmxProvenance{
		Package:              "htmx.org",
		Version:              "2.0.8",
		Registry:             "https://registry.npmjs.org",
		TarballURL:           "https://registry.npmjs.org/htmx.org/-/htmx.org-2.0.8.tgz",
		ArtifactPath:         "package/dist/htmx.min.js",
		InstalledPath:        "internal/dashboard/assets/htmx.min.js",
		License:              "0BSD",
		LicenseInstalledPath: "third_party/htmx/LICENSE",
	}
	if p.Package != want.Package {
		t.Errorf("package = %q, want %q", p.Package, want.Package)
	}
	if p.Version != want.Version {
		t.Errorf("version = %q, want %q", p.Version, want.Version)
	}
	if p.Registry != want.Registry {
		t.Errorf("registry = %q, want %q", p.Registry, want.Registry)
	}
	if p.TarballURL != want.TarballURL {
		t.Errorf("tarball_url = %q, want %q", p.TarballURL, want.TarballURL)
	}
	if p.ArtifactPath != want.ArtifactPath {
		t.Errorf("artifact_path = %q, want %q", p.ArtifactPath, want.ArtifactPath)
	}
	if p.InstalledPath != want.InstalledPath {
		t.Errorf("installed_path = %q, want %q", p.InstalledPath, want.InstalledPath)
	}
	if p.License != want.License {
		t.Errorf("license = %q, want %q", p.License, want.License)
	}
	if p.LicenseInstalledPath != want.LicenseInstalledPath {
		t.Errorf("license_installed_path = %q, want %q", p.LicenseInstalledPath, want.LicenseInstalledPath)
	}
	if len(p.LicenseSHA256) != sha256.Size*2 {
		t.Errorf("license_sha256 = %q, want a %d-character hex digest", p.LicenseSHA256, sha256.Size*2)
	}

	body, err := assetFS.ReadFile("assets/htmx.min.js")
	if err != nil {
		t.Fatalf("read embedded htmx.min.js: %v", err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != p.ArtifactSHA256 {
		t.Errorf("embedded htmx.min.js sha256 = %s, provenance records %s", got, p.ArtifactSHA256)
	}
}

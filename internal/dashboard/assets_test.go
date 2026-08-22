package dashboard

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// assetProvenance mirrors the string fields of an assets/<name>.provenance.json
// file, the canonical record of where a vendored asset came from. The same
// files drive scripts/vendor-assets.sh, so the pins cannot drift between the
// script, the test, and the embedded bytes.
type assetProvenance struct {
	Package              string `json:"package"`
	Version              string `json:"version"`
	Registry             string `json:"registry"`
	TarballURL           string `json:"tarball_url"`
	TarballIntegrity     string `json:"tarball_integrity"`
	TarballShasum        string `json:"tarball_shasum"`
	ArtifactPath         string `json:"artifact_path"`
	InstalledPath        string `json:"installed_path"`
	ArtifactSHA256       string `json:"artifact_sha256"`
	License              string `json:"license"`
	LicenseInstalledPath string `json:"license_installed_path"`
	LicenseSHA256        string `json:"license_sha256"`
}

// vendored lists every third-party asset compiled into the binary, with the
// pins its provenance record is expected to hold. The digests are not repeated
// here: the record is the source of truth for those, and the test checks the
// embedded and checked-in bytes against it.
var vendored = []struct {
	name string
	want assetProvenance
}{
	{
		name: "htmx",
		want: assetProvenance{
			Package:              "htmx.org",
			Version:              "2.0.8",
			Registry:             "https://registry.npmjs.org",
			TarballURL:           "https://registry.npmjs.org/htmx.org/-/htmx.org-2.0.8.tgz",
			TarballIntegrity:     "sha512-fm297iru0iWsNJlBrjvtN7V9zjaxd+69Oqjh4F/Vq9Wwi2kFisLcrLCiv5oBX0KLfOX/zG8AUo9ROMU5XUB44Q==",
			TarballShasum:        "8ac8ba87c141b7bfda7576117476062eeb4aceda",
			ArtifactPath:         "package/dist/htmx.min.js",
			InstalledPath:        "internal/dashboard/assets/htmx.min.js",
			License:              "0BSD",
			LicenseInstalledPath: "third_party/htmx/LICENSE",
		},
	},
	{
		name: "idiomorph",
		want: assetProvenance{
			Package:              "idiomorph",
			Version:              "0.7.4",
			Registry:             "https://registry.npmjs.org",
			TarballURL:           "https://registry.npmjs.org/idiomorph/-/idiomorph-0.7.4.tgz",
			TarballIntegrity:     "sha512-uCdSpLo3uMfqOmrwXTpR1k/sq4sSmKC7l4o/LdJOEU+MMMq+wkevRqOQYn3lP7vfz9Mv+USBEqPvi0XhdL9ENw==",
			TarballShasum:        "5e6ed61ca41114c90e69c5c633c343fdffb79383",
			ArtifactPath:         "package/dist/idiomorph.min.js",
			InstalledPath:        "internal/dashboard/assets/idiomorph.min.js",
			License:              "0BSD",
			LicenseInstalledPath: "third_party/idiomorph/LICENSE",
		},
	},
}

// TestProvenanceMatchesVendoredAssets binds each provenance record to the bytes
// actually compiled into the binary and checked into the tree: the metadata
// names the expected package, the embedded <name>.min.js hashes to exactly the
// recorded artifact digest, and the license file hashes to the recorded
// license digest.
func TestProvenanceMatchesVendoredAssets(t *testing.T) {
	for _, asset := range vendored {
		t.Run(asset.name, func(t *testing.T) {
			raw, err := assetFS.ReadFile("assets/" + asset.name + ".provenance.json")
			if err != nil {
				t.Fatalf("read embedded provenance: %v", err)
			}

			var p assetProvenance
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatalf("decode provenance: %v", err)
			}

			want := asset.want
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
			if p.TarballIntegrity != want.TarballIntegrity {
				t.Errorf("tarball_integrity = %q, want %q", p.TarballIntegrity, want.TarballIntegrity)
			}
			if encoded, ok := strings.CutPrefix(p.TarballIntegrity, "sha512-"); !ok {
				t.Errorf("tarball_integrity = %q, want a sha512 value", p.TarballIntegrity)
			} else if digest, err := base64.StdEncoding.DecodeString(encoded); err != nil || len(digest) != 64 {
				t.Errorf("tarball_integrity has an invalid SHA-512 digest: %v", err)
			}
			if p.TarballShasum != want.TarballShasum {
				t.Errorf("tarball_shasum = %q, want %q", p.TarballShasum, want.TarballShasum)
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

			body, err := assetFS.ReadFile("assets/" + asset.name + ".min.js")
			if err != nil {
				t.Fatalf("read embedded %s.min.js: %v", asset.name, err)
			}
			sum := sha256.Sum256(body)
			if got := hex.EncodeToString(sum[:]); got != p.ArtifactSHA256 {
				t.Errorf("embedded %s.min.js sha256 = %s, provenance records %s", asset.name, got, p.ArtifactSHA256)
			}

			license, err := os.ReadFile(filepath.Join("../..", want.LicenseInstalledPath))
			if err != nil {
				t.Fatalf("read checked-in license: %v", err)
			}
			sum = sha256.Sum256(license)
			if got := hex.EncodeToString(sum[:]); got != p.LicenseSHA256 {
				t.Errorf("%s sha256 = %s, provenance records %s", want.LicenseInstalledPath, got, p.LicenseSHA256)
			}
		})
	}
}

// TestRefreshAuthenticatesBeforeExtraction drives the real shell script in an
// isolated copy of its expected directory layout. A tampered download must be
// rejected by the SHA-512 pin before tar parses it, and neither installed file
// may be touched.
func TestRefreshAuthenticatesBeforeExtraction(t *testing.T) {
	for _, name := range []string{"sh", "curl", "tar", "openssl", "base64"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is required to exercise vendor-assets.sh", name)
		}
	}

	root := t.TempDir()
	for _, dir := range []string{
		"scripts",
		"internal/dashboard/assets",
		"third_party/htmx",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	script, err := os.ReadFile("../../scripts/vendor-assets.sh")
	if err != nil {
		t.Fatalf("read vendoring script: %v", err)
	}
	scriptPath := filepath.Join(root, "scripts/vendor-assets.sh")
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		t.Fatalf("copy vendoring script: %v", err)
	}

	archivePath := filepath.Join(root, "tampered.tgz")
	if err := os.WriteFile(archivePath, []byte("not the pinned archive"), 0o644); err != nil {
		t.Fatalf("write tampered archive: %v", err)
	}
	trusted := sha512.Sum512([]byte("the pinned archive"))
	provenance := map[string]string{
		"package":                "htmx.org",
		"version":                "test",
		"registry":               "file://",
		"tarball_url":            "file://" + archivePath,
		"tarball_integrity":      "sha512-" + base64.StdEncoding.EncodeToString(trusted[:]),
		"tarball_shasum":         strings.Repeat("0", 40),
		"artifact_path":          "package/dist/htmx.min.js",
		"installed_path":         "internal/dashboard/assets/htmx.min.js",
		"artifact_sha256":        strings.Repeat("0", 64),
		"license":                "0BSD",
		"license_artifact_path":  "package/LICENSE",
		"license_installed_path": "third_party/htmx/LICENSE",
		"license_sha256":         strings.Repeat("0", 64),
	}
	raw, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		t.Fatalf("encode provenance: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/dashboard/assets/htmx.provenance.json"), raw, 0o644); err != nil {
		t.Fatalf("write provenance: %v", err)
	}

	assetPath := filepath.Join(root, "internal/dashboard/assets/htmx.min.js")
	licensePath := filepath.Join(root, "third_party/htmx/LICENSE")
	for path, body := range map[string]string{assetPath: "asset sentinel", licensePath: "license sentinel"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write sentinel: %v", err)
		}
	}

	cmd := exec.Command("sh", scriptPath, "--refresh")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("refresh accepted a tampered archive:\n%s", out)
	}
	if !strings.Contains(string(out), "sha512 integrity mismatch") {
		t.Fatalf("refresh failed for the wrong reason; archive may have reached tar:\n%s", out)
	}
	for path, want := range map[string]string{assetPath: "asset sentinel", licensePath: "license sentinel"} {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read sentinel after failed refresh: %v", readErr)
		}
		if string(got) != want {
			t.Errorf("failed refresh changed %s to %q", path, got)
		}
	}
}

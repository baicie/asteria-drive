// Package staging defines the approved, immutable staging release bundles and
// the version-switch state machine used by the staging release controller.
package staging

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	BundleFormat = "asteria-drive-staging-release/v1"
	// Initial allowlist. Adding a version requires a reviewed bundle and an
	// explicit code change; workflow inputs can never introduce a new version.
	VersionV011 = "v0.1.1"
	VersionV020 = "v0.2.0"
)

var (
	supportedVersions = []string{VersionV011, VersionV020}
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imagePattern      = regexp.MustCompile(`^ghcr\.io/baicie/asteria-drive@sha256:[0-9a-f]{64}$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	operations        = map[string]bool{"deploy": true, "rollback": true, "monitor": true, "recovery": true}
	schemaVersion     = 3
)

// Bundle is the complete immutable contract for one approved staging version.
type Bundle struct {
	Format               string   `json:"format"`
	Version              string   `json:"version"`
	SourceCommit         string   `json:"source_commit"`
	Image                string   `json:"image"`
	ComposeSHA256        string   `json:"compose_sha256"`
	DeployScriptSHA256   string   `json:"deploy_script_sha256"`
	MonitorScriptSHA256  string   `json:"monitor_script_sha256"`
	RecoveryScriptSHA256 string   `json:"recovery_script_sha256"`
	SchemaVersion        int      `json:"schema_version"`
	DeploymentEvidence   string   `json:"deployment_evidence_schema"`
	MonitorEvidence      string   `json:"monitor_evidence_schema"`
	RecoveryEvidence     string   `json:"recovery_evidence_schema"`
	Operations           []string `json:"operations"`
	// Paths are populated by LoadBundles and are not serialized.
	Directory string `json:"-"`
}

// BundleSet is the reviewed allowlist loaded from the repository.
type BundleSet map[string]Bundle

func (set BundleSet) Get(version string) (Bundle, bool) {
	bundle, ok := set[version]
	return bundle, ok
}

func (set BundleSet) Versions() []string {
	versions := make([]string, 0, len(set))
	for version := range set {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions
}

// LoadBundles loads exactly the initial allowlist and verifies every file hash.
func LoadBundles(root string) (BundleSet, error) {
	set := make(BundleSet, len(supportedVersions))
	for _, version := range supportedVersions {
		bundle, err := loadBundle(filepath.Join(root, version))
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", version, err)
		}
		if bundle.Version != version {
			return nil, fmt.Errorf("bundle directory %s contains version %s", version, bundle.Version)
		}
		set[version] = bundle
	}
	return set, nil
}

func loadBundle(directory string) (Bundle, error) {
	contents, err := os.ReadFile(filepath.Join(directory, "bundle.json"))
	if err != nil {
		return Bundle{}, fmt.Errorf("read bundle: %w", err)
	}
	var bundle Bundle
	if err := json.Unmarshal(contents, &bundle); err != nil {
		return Bundle{}, fmt.Errorf("decode bundle: %w", err)
	}
	bundle.Directory = directory
	if err := validateBundleMetadata(bundle); err != nil {
		return Bundle{}, err
	}
	for _, file := range []struct {
		name string
		want string
	}{
		{"compose.yaml", bundle.ComposeSHA256},
		{"deploy-staging.sh", bundle.DeployScriptSHA256},
		{"monitor-staging.sh", bundle.MonitorScriptSHA256},
		{"recover-staging.sh", bundle.RecoveryScriptSHA256},
	} {
		actual, err := fileSHA256(filepath.Join(directory, file.name))
		if err != nil {
			return Bundle{}, err
		}
		if actual != file.want {
			return Bundle{}, fmt.Errorf("%s hash %s does not match bundle %s", file.name, actual, file.want)
		}
	}
	return bundle, nil
}

func validateBundleMetadata(bundle Bundle) error {
	if bundle.Format != BundleFormat {
		return fmt.Errorf("unexpected bundle format %q", bundle.Format)
	}
	if !isSupportedVersion(bundle.Version) {
		return fmt.Errorf("unsupported version %q", bundle.Version)
	}
	if !commitPattern.MatchString(bundle.SourceCommit) {
		return fmt.Errorf("invalid source commit")
	}
	if !imagePattern.MatchString(bundle.Image) {
		return fmt.Errorf("invalid OCI image reference")
	}
	for name, value := range map[string]string{
		"compose_sha256": bundle.ComposeSHA256, "deploy_script_sha256": bundle.DeployScriptSHA256,
		"monitor_script_sha256": bundle.MonitorScriptSHA256, "recovery_script_sha256": bundle.RecoveryScriptSHA256,
	} {
		if !digestPattern.MatchString(value) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	if bundle.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported schema version %d", bundle.SchemaVersion)
	}
	if bundle.DeploymentEvidence != "asteria-drive-staging-deployment/v2" ||
		bundle.MonitorEvidence != "asteria-drive-staging-monitor/v2" ||
		bundle.RecoveryEvidence != "asteria-drive-staging-recovery/v2" {
		return fmt.Errorf("unexpected evidence schema")
	}
	if len(bundle.Operations) != len(operations) {
		return fmt.Errorf("bundle operations must be the complete approved set")
	}
	seen := make(map[string]bool, len(bundle.Operations))
	for _, operation := range bundle.Operations {
		if !operations[operation] || seen[operation] {
			return fmt.Errorf("invalid or duplicate operation %q", operation)
		}
		seen[operation] = true
	}
	return nil
}

func isSupportedVersion(version string) bool {
	for _, supported := range supportedVersions {
		if version == supported {
			return true
		}
	}
	return false
}

func fileSHA256(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateActive checks a real host snapshot against the bundle. It is the
// version-aware replacement for fixed latest-version checks.
func (bundle Bundle) ValidateActive(snapshot ActiveState) error {
	if snapshot.Version != bundle.Version {
		return fmt.Errorf("active version %q is not bundle %q", snapshot.Version, bundle.Version)
	}
	if snapshot.ComposeSHA256 != bundle.ComposeSHA256 {
		return fmt.Errorf("active Compose hash %s does not match %s", snapshot.ComposeSHA256, bundle.ComposeSHA256)
	}
	if snapshot.Image != bundle.Image {
		return fmt.Errorf("active image %s does not match %s", snapshot.Image, bundle.Image)
	}
	if snapshot.SchemaVersion != bundle.SchemaVersion {
		return fmt.Errorf("active schema %d does not match %d", snapshot.SchemaVersion, bundle.SchemaVersion)
	}
	if !snapshot.Healthy {
		return fmt.Errorf("active version %s is not healthy", snapshot.Version)
	}
	return nil
}

// String is used in evidence and error messages without exposing host secrets.
func (bundle Bundle) String() string {
	return strings.Join([]string{bundle.Version, bundle.SourceCommit, bundle.Image}, " ")
}

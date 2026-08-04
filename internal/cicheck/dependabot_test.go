package cicheck

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

type dependabotDocument struct {
	Version int                `yaml:"version"`
	Updates []dependabotUpdate `yaml:"updates"`
}

type dependabotUpdate struct {
	Ecosystem             string `yaml:"package-ecosystem"`
	Directory             string `yaml:"directory"`
	OpenPullRequestsLimit int    `yaml:"open-pull-requests-limit"`
	Schedule              struct {
		Interval string `yaml:"interval"`
	} `yaml:"schedule"`
}

func TestDependabotCoversRepositoryDependencySurfaces(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../.github/dependabot.yml")
	if err != nil {
		t.Fatalf("read Dependabot configuration: %v", err)
	}
	var config dependabotDocument
	if err := yaml.Unmarshal(contents, &config); err != nil {
		t.Fatalf("parse Dependabot configuration: %v", err)
	}
	if config.Version != 2 {
		t.Errorf("Dependabot config version = %d, want 2", config.Version)
	}
	expectedEcosystems := map[string]struct{}{
		"gomod":          {},
		"npm":            {},
		"github-actions": {},
		"docker":         {},
	}
	if len(config.Updates) != len(expectedEcosystems) {
		t.Fatalf("Dependabot has %d update rules, want %d", len(config.Updates), len(expectedEcosystems))
	}
	for _, update := range config.Updates {
		if _, ok := expectedEcosystems[update.Ecosystem]; !ok {
			t.Errorf("Dependabot contains unexpected ecosystem %q", update.Ecosystem)
		}
		if update.Directory != "/" {
			t.Errorf("Dependabot %s directory = %q, want /", update.Ecosystem, update.Directory)
		}
		if update.Schedule.Interval != "weekly" {
			t.Errorf("Dependabot %s interval = %q, want weekly", update.Ecosystem, update.Schedule.Interval)
		}
		if update.OpenPullRequestsLimit != 5 {
			t.Errorf("Dependabot %s open PR limit = %d, want 5", update.Ecosystem, update.OpenPullRequestsLimit)
		}
	}
}

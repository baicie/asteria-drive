package cicheck

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type IntegrationManifest struct {
	Packages []IntegrationPackage `json:"packages"`
}

type IntegrationPackage struct {
	Package string   `json:"package"`
	Tests   []string `json:"tests"`
}

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

func LoadIntegrationManifest(reader io.Reader) (IntegrationManifest, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest IntegrationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return IntegrationManifest{}, fmt.Errorf("decode integration manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return IntegrationManifest{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return IntegrationManifest{}, err
	}
	return manifest, nil
}

func VerifyIntegrationReport(reader io.Reader, manifest IntegrationManifest) error {
	required := make(map[string]map[string]string, len(manifest.Packages))
	packageStatus := make(map[string]string, len(manifest.Packages))
	for _, requirement := range manifest.Packages {
		required[requirement.Package] = make(map[string]string, len(requirement.Tests))
		for _, test := range requirement.Tests {
			required[requirement.Package][test] = "missing"
		}
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var event testEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode go test event at line %d: %w", line, err)
		}
		if event.Action == "skip" && event.Test != "" {
			return fmt.Errorf("integration report contains skipped test %s %s", event.Package, event.Test)
		}
		if event.Test == "" {
			if _, ok := required[event.Package]; ok && (event.Action == "pass" || event.Action == "fail" || event.Action == "skip") {
				packageStatus[event.Package] = event.Action
			}
			continue
		}
		if tests, ok := required[event.Package]; ok {
			if _, requiredTest := tests[event.Test]; requiredTest && (event.Action == "pass" || event.Action == "fail" || event.Action == "skip") {
				tests[event.Test] = event.Action
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read go test report: %w", err)
	}

	var failures []string
	for _, requirement := range manifest.Packages {
		if status := packageStatus[requirement.Package]; status != "pass" {
			failures = append(failures, fmt.Sprintf("package %s status=%s", requirement.Package, statusOrMissing(status)))
		}
		for _, test := range requirement.Tests {
			if status := required[requirement.Package][test]; status != "pass" {
				failures = append(failures, fmt.Sprintf("test %s %s status=%s", requirement.Package, test, statusOrMissing(status)))
			}
		}
	}
	if len(failures) != 0 {
		sort.Strings(failures)
		return fmt.Errorf("integration evidence is incomplete:\n%s", strings.Join(failures, "\n"))
	}
	return nil
}

func validateManifest(manifest IntegrationManifest) error {
	if len(manifest.Packages) == 0 {
		return fmt.Errorf("integration manifest must contain at least one package")
	}
	packages := make(map[string]struct{}, len(manifest.Packages))
	for _, requirement := range manifest.Packages {
		if requirement.Package == "" || len(requirement.Tests) == 0 {
			return fmt.Errorf("integration manifest package and tests are required")
		}
		if _, duplicate := packages[requirement.Package]; duplicate {
			return fmt.Errorf("integration manifest repeats package %s", requirement.Package)
		}
		packages[requirement.Package] = struct{}{}
		tests := make(map[string]struct{}, len(requirement.Tests))
		for _, test := range requirement.Tests {
			if test == "" {
				return fmt.Errorf("integration manifest contains an empty test for package %s", requirement.Package)
			}
			if _, duplicate := tests[test]; duplicate {
				return fmt.Errorf("integration manifest repeats test %s %s", requirement.Package, test)
			}
			tests[test] = struct{}{}
		}
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("integration manifest must contain one JSON value")
		}
		return fmt.Errorf("decode trailing integration manifest data: %w", err)
	}
	return nil
}

func statusOrMissing(status string) string {
	if status == "" {
		return "missing"
	}
	return status
}

package cicheck

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	sbomAction             = "anchore/sbom-action@e22c389904149dbc22b58101806040fa8d37a610"
	downloadArtifactAction = "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093"
	attestProvenanceAction = "actions/attest-build-provenance@0f67c3f4856b2e3261c31976d6725780e5e4c373"
	setupQEMUAction        = "docker/setup-qemu-action@c7c53464625b32c7a7e944ae62b3e17d2b600130"
	setupBuildxAction      = "docker/setup-buildx-action@8d2750c68a42422c14e847fe6c8ac0403b4cbd6f"
	registryLoginAction    = "docker/login-action@c94ce9fb468520275223c153574b00df6fe4bcc9"
	buildPushAction        = "docker/build-push-action@10e90e3645eae34f1e60eeb005ba3a3d33f178e8"
)

type releaseWorkflowDocument struct {
	Name        string               `yaml:"name"`
	On          map[string]yaml.Node `yaml:"on"`
	Permissions map[string]string    `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress bool   `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]releaseWorkflowJob `yaml:"jobs"`
}

type releaseWorkflowJob struct {
	Name        string            `yaml:"name"`
	Needs       string            `yaml:"needs"`
	RunsOn      string            `yaml:"runs-on"`
	Timeout     int               `yaml:"timeout-minutes"`
	Environment string            `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	Outputs     map[string]string `yaml:"outputs"`
	Steps       []workflowStep    `yaml:"steps"`
}

func TestReleaseWorkflowTrustBoundaryAndArtifactContract(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	text := string(contents)
	if strings.Contains(text, "pull_request") || strings.Contains(text, "pull_request_target") {
		t.Fatal("release workflow must not run for pull requests")
	}
	if strings.Contains(text, "secrets.") {
		t.Fatal("release workflow must not read repository secrets")
	}

	var workflow releaseWorkflowDocument
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	if workflow.Name != "Release" {
		t.Fatalf("workflow name = %q, want Release", workflow.Name)
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatalf("workflow permissions = %#v, want contents: read", workflow.Permissions)
	}
	if len(workflow.On) != 2 {
		t.Fatalf("workflow triggers = %#v, want tag push and workflow_dispatch only", workflow.On)
	}
	if _, ok := workflow.On["push"]; !ok {
		t.Error("release workflow is missing push trigger")
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Error("release workflow is missing workflow_dispatch trigger")
	}
	if workflow.Concurrency.CancelInProgress {
		t.Error("release workflow must not cancel an in-progress release")
	}
	if len(workflow.Jobs) != 2 {
		t.Fatalf("workflow has %d jobs, want build and publish", len(workflow.Jobs))
	}

	build, ok := workflow.Jobs["build"]
	if !ok {
		t.Fatal("release workflow is missing build job")
	}
	if build.Name != "Release / build" || build.RunsOn != "ubuntu-24.04" || build.Timeout != 20 {
		t.Errorf("build job metadata = %#v", build)
	}
	if len(build.Permissions) != 0 {
		t.Errorf("build job must retain read-only permissions: %#v", build.Permissions)
	}
	if !reflect.DeepEqual(build.Outputs, map[string]string{
		"tag": "${{ steps.release-input.outputs.tag }}", "version": "${{ steps.release-input.outputs.version }}", "commit": "${{ steps.release-input.outputs.commit }}", "build_time": "${{ steps.release-input.outputs.build_time }}",
	}) {
		t.Errorf("build outputs = %#v", build.Outputs)
	}
	assertPinnedActions(t, build.Steps)
	buildActions := workflowActions(build.Steps)
	if !reflect.DeepEqual(buildActions, []string{checkoutAction, setupGoAction, sbomAction, uploadArtifactAction}) {
		t.Errorf("build actions = %#v", buildActions)
	}
	buildCommands := workflowCommands(build.Steps)
	for _, required := range []string{
		"git merge-base --is-ancestor",
		"go run ./tools/package-release",
		"go run ./tools/release-checksums",
	} {
		if !strings.Contains(buildCommands, required) {
			t.Errorf("build job is missing %q", required)
		}
	}

	publish, ok := workflow.Jobs["publish"]
	if !ok {
		t.Fatal("release workflow is missing publish job")
	}
	if publish.Name != "Release / publish" || publish.Needs != "build" || publish.Environment != "release" || publish.RunsOn != "ubuntu-24.04" {
		t.Errorf("publish job metadata = %#v", publish)
	}
	if !reflect.DeepEqual(publish.Permissions, map[string]string{
		"contents": "write", "id-token": "write", "attestations": "write", "packages": "write",
	}) {
		t.Errorf("publish permissions = %#v", publish.Permissions)
	}
	assertPinnedActions(t, publish.Steps)
	publishActions := workflowActions(publish.Steps)
	if !reflect.DeepEqual(publishActions, []string{
		checkoutAction, downloadArtifactAction, setupQEMUAction, setupBuildxAction,
		registryLoginAction, buildPushAction, attestProvenanceAction, attestProvenanceAction,
	}) {
		t.Errorf("publish actions = %#v", publishActions)
	}
	publishCommands := workflowCommands(publish.Steps)
	for _, required := range []string{"sha256sum --check checksums.txt", "gh release create"} {
		if !strings.Contains(publishCommands, required) {
			t.Errorf("publish job is missing %q", required)
		}
	}
	attest := findWorkflowStep(publish.Steps, attestProvenanceAction)
	if attest == nil || fmtString(attest.With["subject-checksums"]) != "${{ runner.temp }}/asteria-release/checksums.txt" {
		t.Errorf("publish job must attest checksums.txt: %#v", attest)
	}
	image := findWorkflowStep(publish.Steps, buildPushAction)
	if image == nil || fmtString(image.With["platforms"]) != "linux/amd64,linux/arm64" || image.With["push"] != true {
		t.Errorf("publish job must push a multi-architecture image: %#v", image)
	}
	if !strings.Contains(fmtString(image.With["tags"]), "sha-${{ needs.build.outputs.commit }}") {
		t.Errorf("published image must include an immutable commit tag: %#v", image.With["tags"])
	}
	if image == nil || image.With["provenance"] != false || strings.Contains(fmtString(image.With["tags"]), ":latest") {
		t.Errorf("published image must rely on the explicit digest attestation and never publish latest: %#v", image)
	}
	imageAttestation := findWorkflowStepNamed(publish.Steps, "Attest OCI image")
	if imageAttestation == nil || imageAttestation.Uses != attestProvenanceAction ||
		fmtString(imageAttestation.With["subject-name"]) != "${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}" ||
		fmtString(imageAttestation.With["subject-digest"]) != "${{ steps.publish-image.outputs.digest }}" ||
		imageAttestation.With["push-to-registry"] != true {
		t.Errorf("publish job must attest the pushed OCI manifest digest: %#v", imageAttestation)
	}
}

func assertPinnedActions(t *testing.T, steps []workflowStep) {
	t.Helper()
	for _, step := range steps {
		if step.Uses != "" && !pinnedAction.MatchString(step.Uses) {
			t.Errorf("mutable Action reference %q", step.Uses)
		}
	}
}

func workflowActions(steps []workflowStep) []string {
	var actions []string
	for _, step := range steps {
		if step.Uses != "" {
			actions = append(actions, step.Uses)
		}
	}
	return actions
}

func workflowCommands(steps []workflowStep) string {
	var commands []string
	for _, step := range steps {
		if step.Run != "" {
			commands = append(commands, step.Run)
		}
	}
	return strings.Join(commands, "\n")
}

func findWorkflowStep(steps []workflowStep, uses string) *workflowStep {
	for index := range steps {
		if steps[index].Uses == uses {
			return &steps[index]
		}
	}
	return nil
}

func findWorkflowStepNamed(steps []workflowStep, name string) *workflowStep {
	for index := range steps {
		if steps[index].Name == name {
			return &steps[index]
		}
	}
	return nil
}

func fmtString(value any) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(toString(value)), "\r\n", "\n"))
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	return value.(string)
}

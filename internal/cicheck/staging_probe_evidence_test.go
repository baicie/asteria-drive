package cicheck

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// probeEvidencePython returns an interpreter that can actually run a script.
// Windows ships a python3.exe store shim that exits 49 without running anything,
// so the name alone is not evidence that Python works.
func probeEvidencePython(t *testing.T) string {
	t.Helper()
	candidates := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		candidates = []string{"python", "py"}
	}
	for _, name := range candidates {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		args := []string{"-c", "print('ok')"}
		if name == "py" {
			args = append([]string{"-3"}, args...)
		}
		if output, err := exec.Command(path, args...).CombinedOutput(); err == nil &&
			strings.TrimSpace(string(output)) == "ok" {
			return path
		}
	}
	t.Skip("no working Python interpreter is available")
	return ""
}

// stagingProbeEvidenceBlock extracts the inline sanitizer that both staging
// workflows run after every SSH probe attempt. Keeping it inline is deliberate:
// the monitor workflow is contractually forbidden from checking out the
// repository, so it cannot call a script file.
func stagingProbeEvidenceBlock(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// .gitattributes forces LF in the repository, but a Windows checkout may hand
	// the file back with CRLF; both forms must parse identically.
	text := strings.ReplaceAll(string(contents), "\r\n", "\n")
	const start = "# --- asteria-probe-evidence:v1 ---"
	const end = "# --- end asteria-probe-evidence ---"
	first := strings.Index(text, start)
	if first < 0 {
		t.Fatalf("%s is missing the probe evidence block", path)
	}
	last := strings.Index(text, end)
	if last < first {
		t.Fatalf("%s has an unterminated probe evidence block", path)
	}
	// Widen the slice to whole lines so the YAML indentation is part of the block.
	first = strings.LastIndex(text[:first], "\n") + 1
	if newline := strings.Index(text[last:], "\n"); newline >= 0 {
		last += newline
	} else {
		last = len(text)
	}
	block := strings.TrimSuffix(text[first:last], "\n")
	// The block is embedded in a YAML literal block, so every line carries the
	// workflow's indentation; strip exactly that.
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		if line != "" && !strings.HasPrefix(line, "          ") {
			t.Fatalf("%s probe evidence line has unexpected indentation: %q", path, line)
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	return strings.Join(lines, "\n")
}

func TestStagingWorkflowsRetainSanitizedProbeEvidence(t *testing.T) {
	t.Parallel()
	python := probeEvidencePython(t)

	for _, workflow := range []string{"monitor-staging.yml", "drill-staging-recovery.yml"} {
		workflow := workflow
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "..", ".github", "workflows", workflow)
			block := stagingProbeEvidenceBlock(t, path)
			evidence := t.TempDir()

			raw := filepath.Join(evidence, "raw.json")
			stderr := filepath.Join(evidence, "remote-stderr.log")
			if err := os.WriteFile(raw, []byte(`{"status":"failed","last_phase":"compose-identity"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			secret := "postgres://asteria:supersecret@10.9.8.7:5432/asteria refused\n" +
				"token=deadbeefdeadbeef token still set\n"
			if err := os.WriteFile(stderr, []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}

			command := exec.Command(python, "-")
			command.Stdin = strings.NewReader(block)
			command.Env = append(os.Environ(),
				"PROBE_EVIDENCE_DIR="+evidence,
				"PROBE_CANDIDATE=v0.2.0",
				"PROBE_EXIT_CODE=1",
				"PROBE_EVIDENCE_PATH="+raw,
				"PROBE_STDERR_PATH="+stderr,
				"PROBE_DEPLOY_HOST=staging.example.internal",
			)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("probe evidence block failed: %v\n%s", err, output)
			}

			attempt, err := os.ReadFile(filepath.Join(evidence, "attempt-v0.2.0.json"))
			if err != nil {
				t.Fatalf("attempt record was not written: %v", err)
			}
			var report struct {
				Schema    string `json:"schema"`
				Candidate string `json:"candidate"`
				ExitCode  int    `json:"exit_code"`
				Stderr    struct {
					Present   bool   `json:"present"`
					SizeBytes int    `json:"size_bytes"`
					SHA256    string `json:"sha256"`
				} `json:"stderr"`
			}
			if err := json.Unmarshal(attempt, &report); err != nil {
				t.Fatalf("attempt record is not valid JSON: %v", err)
			}
			if report.Schema != "asteria-drive-staging-probe-attempt/v1" {
				t.Errorf("attempt schema = %q", report.Schema)
			}
			if report.Candidate != "v0.2.0" || report.ExitCode != 1 {
				t.Errorf("attempt identity = %q/%d", report.Candidate, report.ExitCode)
			}
			if !report.Stderr.Present || report.Stderr.SizeBytes != len(secret) || len(report.Stderr.SHA256) != 64 {
				t.Errorf("attempt stderr description = %#v", report.Stderr)
			}

			excerptText, err := os.ReadFile(filepath.Join(evidence, "remote-stderr-excerpt.txt"))
			if err != nil {
				t.Fatalf("redacted excerpt was not written: %v", err)
			}
			excerpt := string(excerptText)
			if !strings.Contains(excerpt, "refused") || !strings.Contains(excerpt, "still set") {
				t.Errorf("excerpt dropped the failure signal: %q", excerpt)
			}
			for _, forbidden := range []string{"supersecret", "10.9.8.7", "deadbeefdeadbeef", "staging.example.internal"} {
				if strings.Contains(excerpt, forbidden) {
					t.Errorf("excerpt leaked %q: %q", forbidden, excerpt)
				}
			}
			if !strings.Contains(excerpt, "# v0.2.0 exit=1") {
				t.Errorf("excerpt is not bound to the attempt: %q", excerpt)
			}
		})
	}
}

func TestStagingProbeEvidenceRejectsUnsafeCandidates(t *testing.T) {
	t.Parallel()
	python := probeEvidencePython(t)
	block := stagingProbeEvidenceBlock(t, filepath.Join("..", "..", ".github", "workflows", "monitor-staging.yml"))
	evidence := t.TempDir()
	for _, candidate := range []string{"../../etc/passwd", "v0.2.0/../x", ""} {
		command := exec.Command(python, "-")
		command.Stdin = strings.NewReader(block)
		command.Env = append(os.Environ(),
			"PROBE_EVIDENCE_DIR="+evidence,
			"PROBE_CANDIDATE="+candidate,
			"PROBE_EXIT_CODE=1",
			"PROBE_EVIDENCE_PATH="+filepath.Join(evidence, "absent.json"),
			"PROBE_STDERR_PATH="+filepath.Join(evidence, "absent.log"),
		)
		if output, err := command.CombinedOutput(); err == nil {
			t.Errorf("candidate %q was accepted: %s", candidate, output)
		}
	}
	entries, err := os.ReadDir(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected candidates still wrote %d files", len(entries))
	}
}

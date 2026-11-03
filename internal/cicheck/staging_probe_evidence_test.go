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

// stagingProbeClassifier holds the pieces of an attempt record that the
// workflows are allowed to derive from remote-controlled input.
type stagingProbeClassifier struct {
	Schema         string  `json:"schema"`
	Candidate      string  `json:"candidate"`
	ExitCode       int     `json:"exit_code"`
	Classification string  `json:"classification"`
	Phase          *string `json:"phase"`
	Evidence       struct {
		Present   bool   `json:"present"`
		SizeBytes int    `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	} `json:"evidence"`
	Stderr struct {
		Present   bool   `json:"present"`
		SizeBytes int    `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	} `json:"stderr"`
}

// probeEvidencePython returns an interpreter that can actually run a script.
// Windows ships a python3.exe store shim that exits 49 without running anything,
// so the name alone is not evidence that Python works.
//
// On Linux — the platform both workflows run on — a missing interpreter fails the
// test instead of skipping it: these are security regressions, and a skip would
// silently drop them from the gate.
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
	if runtime.GOOS == "linux" {
		t.Fatalf("no working Python interpreter: the staging probe classifier cannot be verified")
	}
	t.Skip("no working Python interpreter is available on this platform")
	return ""
}

// stagingProbeEvidenceBlock extracts the inline classifier that both staging
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
	const start = "# --- asteria-staging-probe-attempt:v2 ---"
	const end = "# --- end asteria-staging-probe-attempt ---"
	first := strings.Index(text, start)
	if first < 0 {
		t.Fatalf("%s is missing the probe attempt block", path)
	}
	last := strings.Index(text, end)
	if last < first {
		t.Fatalf("%s has an unterminated probe attempt block", path)
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
			t.Fatalf("%s probe attempt line has unexpected indentation: %q", path, line)
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	return strings.Join(lines, "\n")
}

func runProbeEvidenceBlock(t *testing.T, python, block, dir string, env map[string]string) (string, error) {
	t.Helper()
	command := exec.Command(python, "-")
	command.Stdin = strings.NewReader(block)
	command.Env = append(os.Environ(),
		"PROBE_EVIDENCE_DIR="+dir,
		"PROBE_DEPLOY_HOST=staging.internal.example",
	)
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func readProbeAttempt(t *testing.T, dir, candidate string) stagingProbeClassifier {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "attempt-"+candidate+".json"))
	if err != nil {
		t.Fatalf("attempt record was not written: %v", err)
	}
	var record stagingProbeClassifier
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("attempt record is not valid JSON: %v", err)
	}
	return record
}

func TestStagingProbeClassifierWhitelist(t *testing.T) {
	t.Parallel()
	python := probeEvidencePython(t)
	block := stagingProbeEvidenceBlock(t, filepath.Join("..", "..", ".github", "workflows", "monitor-staging.yml"))

	cases := []struct {
		name           string
		exitCode       string
		evidence       string
		stderr         string
		classification string
		phase          string
	}{
		{
			name:           "dispatcher rejection",
			exitCode:       "1",
			evidence:       "",
			stderr:         "staging deploy command rejected\n",
			classification: "dispatcher-rejected",
		},
		{
			name:           "ssh authentication",
			exitCode:       "255",
			stderr:         "Permission denied (publickey).\n",
			classification: "ssh-auth-failed",
		},
		{
			name:           "ssh host key",
			exitCode:       "255",
			stderr:         "Host key verification failed.\n",
			classification: "ssh-host-key-failed",
		},
		{
			name:           "ssh connect",
			exitCode:       "255",
			stderr:         "ssh: connect to host example.invalid port 22: Connection refused\n",
			classification: "ssh-connect-failed",
		},
		{
			name:           "compose identity phase",
			exitCode:       "1",
			evidence:       `{"status":"failed","last_phase":"compose-identity"}`,
			classification: "compose-identity-failed",
			phase:          "compose-identity",
		},
		{
			name:           "source identity phase",
			exitCode:       "1",
			evidence:       `{"status":"failed","last_phase":"source-identity"}`,
			classification: "source-identity-failed",
			phase:          "source-identity",
		},
		{
			name:           "capacity phase",
			exitCode:       "1",
			evidence:       `{"status":"failed","last_phase":"host-capacity"}`,
			classification: "capacity-guard-failed",
			phase:          "host-capacity",
		},
		{
			name:           "later declared phase",
			exitCode:       "1",
			evidence:       `{"status":"failed","last_phase":"http-probes"}`,
			classification: "remote-command-failed",
			phase:          "http-probes",
		},
		{
			name:           "unclassified failure keeps only digests",
			exitCode:       "1",
			evidence:       "",
			stderr:         "some unexpected remote failure\n",
			classification: "unknown",
		},
		{
			name:           "undeclared phase is dropped",
			exitCode:       "1",
			evidence:       `{"status":"failed","last_phase":"../../etc/shadow"}`,
			classification: "unknown",
		},
		{
			name:           "success records no stderr text",
			exitCode:       "0",
			evidence:       `{"status":"success","last_phase":"complete"}`,
			stderr:         "warning: something\n",
			classification: "success",
			phase:          "complete",
		},
	}

	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			evidencePath := filepath.Join(dir, "raw.json")
			stderrPath := filepath.Join(dir, "remote-stderr.log")
			if test.evidence != "" {
				if err := os.WriteFile(evidencePath, []byte(test.evidence+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.stderr != "" {
				if err := os.WriteFile(stderrPath, []byte(test.stderr), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			output, err := runProbeEvidenceBlock(t, python, block, dir, map[string]string{
				"PROBE_CANDIDATE":     "v0.2.0",
				"PROBE_EXIT_CODE":     test.exitCode,
				"PROBE_EVIDENCE_PATH": evidencePath,
				"PROBE_STDERR_PATH":   stderrPath,
			})
			if err != nil {
				t.Fatalf("classifier failed: %v\n%s", err, output)
			}
			record := readProbeAttempt(t, dir, "v0.2.0")
			if record.Schema != "asteria-drive-staging-probe-attempt/v2" {
				t.Errorf("schema = %q", record.Schema)
			}
			if record.Classification != test.classification {
				t.Errorf("classification = %q, want %q", record.Classification, test.classification)
			}
			if test.phase == "" {
				if record.Phase != nil {
					t.Errorf("phase = %q, want none", *record.Phase)
				}
			} else if record.Phase == nil || *record.Phase != test.phase {
				t.Errorf("phase = %v, want %q", record.Phase, test.phase)
			}
			// Every readable string in the artifact must be a fixed-format value.
			for _, forbidden := range []string{"staging.internal.example", "unexpected remote failure", "shadow"} {
				raw, err := os.ReadFile(filepath.Join(dir, "attempt-v0.2.0.json"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), forbidden) {
					t.Errorf("artifact leaked %q: %s", forbidden, raw)
				}
			}
		})
	}
}

// TestStagingProbeClassifierRedactsSensitiveValues is the counterexample matrix:
// each payload must produce only digests, a fixed classification and a bounded
// phase, never any part of the payload.
func TestStagingProbeClassifierRedactsSensitiveValues(t *testing.T) {
	t.Parallel()
	python := probeEvidencePython(t)
	block := stagingProbeEvidenceBlock(t, filepath.Join("..", "..", ".github", "workflows", "monitor-staging.yml"))

	secret := "s3cr3t-Value-9f2a"
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"
	payloads := map[string]string{
		"bearer-jwt":         "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9." + secret + ".c2lnbmF0dXJl\n",
		"aws-secret":         "AWS_SECRET_ACCESS_KEY=" + secret + "\n",
		"access-token-query": "https://api.example.test/upload?access_token=" + secret + "\n",
		"ipv4":               "dial tcp 10.42.7.19:5432: connection refused\n",
		"ipv6":               "dial tcp [2001:db8::1]:5432: connection refused\n",
		"pem":                pem + "\n",
		"private-key":        "private_key=" + secret + "\npassphrase=" + secret + "\n",
		"postgres-dsn":       "postgres://asteria:" + secret + "@db.internal:5432/asteria?sslmode=require\n",
		"ssh-host":           "ssh: Could not resolve hostname staging.internal.example\n",
		"short-random-key":   "key " + secret + " rejected\n",
		"multi-line":         "line one " + secret + "\nline two " + secret + "\n",
	}

	for name, payload := range payloads {
		name, payload := name, payload
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Mirror the real layout: raw files live in $RUNNER_TEMP, only the
			// classifier's own records reach the artifact directory.
			dir := t.TempDir()
			evidenceDir := filepath.Join(dir, "artifact")
			runnerTemp := filepath.Join(dir, "runner-temp")
			for _, path := range []string{evidenceDir, runnerTemp} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			evidencePath := filepath.Join(runnerTemp, "raw.json")
			stderrPath := filepath.Join(runnerTemp, "remote-stderr.log")
			if err := os.WriteFile(evidencePath, []byte(`{"status":"failed"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stderrPath, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := runProbeEvidenceBlock(t, python, block, evidenceDir, map[string]string{
				"PROBE_CANDIDATE":     "v0.2.0",
				"PROBE_EXIT_CODE":     "1",
				"PROBE_EVIDENCE_PATH": evidencePath,
				"PROBE_STDERR_PATH":   stderrPath,
			})
			if err != nil {
				t.Fatalf("classifier failed: %v\n%s", err, output)
			}
			record := readProbeAttempt(t, evidenceDir, "v0.2.0")
			if record.Classification != "unknown" {
				t.Errorf("classification = %q, want unknown for a free-text payload", record.Classification)
			}
			if !record.Stderr.Present || record.Stderr.SizeBytes != len(payload) || len(record.Stderr.SHA256) != 64 {
				t.Errorf("stderr digest = %#v", record.Stderr)
			}
			entries, err := os.ReadDir(evidenceDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) == 0 {
				t.Fatal("classifier wrote nothing")
			}
			for _, entry := range entries {
				content, err := os.ReadFile(filepath.Join(evidenceDir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				for _, needle := range []string{secret, "BEGIN OPENSSH", "10.42.7.19", "2001:db8", "staging.internal.example", "eyJhbGci"} {
					if strings.Contains(string(content), needle) {
						t.Errorf("artifact %s leaked %q: %s", entry.Name(), needle, content)
					}
				}
			}
		})
	}
}

func TestStagingProbeClassifierRejectsUnsafeCandidates(t *testing.T) {
	t.Parallel()
	python := probeEvidencePython(t)
	block := stagingProbeEvidenceBlock(t, filepath.Join("..", "..", ".github", "workflows", "monitor-staging.yml"))
	dir := t.TempDir()
	for _, candidate := range []string{"../../etc/passwd", "v0.2.0/../x", ""} {
		output, err := runProbeEvidenceBlock(t, python, block, dir, map[string]string{
			"PROBE_CANDIDATE":     candidate,
			"PROBE_EXIT_CODE":     "1",
			"PROBE_EVIDENCE_PATH": filepath.Join(dir, "absent.json"),
			"PROBE_STDERR_PATH":   filepath.Join(dir, "absent.log"),
		})
		if err == nil {
			t.Errorf("candidate %q was accepted: %s", candidate, output)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected candidates still wrote %d files", len(entries))
	}
}

// TestStagingWorkflowsShareOneClassifierSchema pins that both workflows embed
// byte-identical classifiers, so the two cannot drift apart.
func TestStagingWorkflowsShareOneClassifierSchema(t *testing.T) {
	t.Parallel()
	monitor := stagingProbeEvidenceBlock(t, filepath.Join("..", "..", ".github", "workflows", "monitor-staging.yml"))
	drill := stagingProbeEvidenceBlock(t, filepath.Join("..", "..", ".github", "workflows", "drill-staging-recovery.yml"))
	if monitor != drill {
		t.Error("monitor and recovery classifiers are not identical")
	}
	for _, required := range []string{
		`SCHEMA = "asteria-drive-staging-probe-attempt/v2"`,
		`"dispatcher-rejected"`,
		`"ssh-auth-failed"`,
		`"ssh-host-key-failed"`,
		`"ssh-connect-failed"`,
		`"compose-identity-failed"`,
		`"source-identity-failed"`,
		`"capacity-guard-failed"`,
		`"remote-command-failed"`,
		`"unknown"`,
		`"success"`,
	} {
		if !strings.Contains(monitor, required) {
			t.Errorf("classifier is missing %q", required)
		}
	}
	// The removed free-text path must not come back.
	for _, forbidden := range []string{"remote-stderr-excerpt", "PROBE_DEPLOY_HOST", "redact(", "MAX_LINES"} {
		if strings.Contains(monitor, forbidden) {
			t.Errorf("classifier still contains %q", forbidden)
		}
	}
}

// TestStagingWorkflowsKeepRawFilesEphemeral guards the boundary the classifier
// relies on: raw remote output is written only to $RUNNER_TEMP and removed by an
// EXIT trap, and only the classified records reach the artifact directory.
func TestStagingWorkflowsKeepRawFilesEphemeral(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"monitor-staging.yml", "drill-staging-recovery.yml"} {
		contents, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := strings.ReplaceAll(string(contents), "\r\n", "\n")
		// The two workflows name their raw stderr variable differently; each must
		// still remove both raw files in an EXIT trap.
		deletesRawOutput := false
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "trap ") || !strings.Contains(line, "rm -f --") {
				continue
			}
			if strings.Contains(line, `"$raw_evidence"`) &&
				(strings.Contains(line, `"$raw_stderr"`) || strings.Contains(line, `"$remote_stderr"`)) {
				deletesRawOutput = true
			}
		}
		if !deletesRawOutput {
			t.Errorf("%s does not delete raw probe output on exit", name)
		}
		if strings.Contains(text, "remote-stderr-excerpt") {
			t.Errorf("%s still writes a free-text stderr excerpt", name)
		}
		for _, line := range strings.Split(text, "\n") {
			// Raw paths must never be pointed at the uploaded evidence directory.
			if strings.Contains(line, "raw_evidence=") || strings.Contains(line, "raw_stderr=") {
				if strings.Contains(line, "evidence_dir") {
					t.Errorf("%s stores raw output inside the artifact directory: %q", name, line)
				}
			}
		}
	}
}

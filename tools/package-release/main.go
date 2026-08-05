package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/baicie/asteria-drive/internal/release"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type target struct {
	arch   string
	goarch string
}

func main() {
	version := flag.String("version", "", "release version, including the v prefix")
	commit := flag.String("commit", "", "40-character source commit")
	epoch := flag.Int64("source-date-epoch", 0, "source date epoch used for deterministic output")
	output := flag.String("output", "", "output directory")
	flag.Parse()
	if err := run(*version, *commit, *epoch, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(version, commit string, epoch int64, output string) error {
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`).MatchString(version) {
		return fmt.Errorf("version must match vMAJOR.MINOR.PATCH[-prerelease]: %q", version)
	}
	if !commitPattern.MatchString(commit) {
		return fmt.Errorf("commit must be a 40-character lowercase SHA: %q", commit)
	}
	if epoch <= 0 {
		return fmt.Errorf("source-date-epoch must be positive")
	}
	if output == "" {
		return fmt.Errorf("output is required")
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		return fmt.Errorf("read output directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("output directory must be empty: %s", output)
	}

	buildDate := time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	ldflags := strings.Join([]string{
		"-s", "-w",
		"-X", "github.com/baicie/asteria-drive/internal/buildinfo.Version=" + version,
		"-X", "github.com/baicie/asteria-drive/internal/buildinfo.Commit=" + commit,
		"-X", "github.com/baicie/asteria-drive/internal/buildinfo.Date=" + buildDate,
	}, " ")
	targets := []target{{arch: "amd64", goarch: "amd64"}, {arch: "arm64", goarch: "arm64"}}
	var artifacts []string
	for _, target := range targets {
		staging, err := os.MkdirTemp(output, ".staging-")
		if err != nil {
			return fmt.Errorf("create staging directory: %w", err)
		}
		serverPath := filepath.Join(staging, "asteria-server")
		migratePath := filepath.Join(staging, "asteria-migrate")
		verifyPath := filepath.Join(staging, "asteria-verify-storage")
		if err := build(context.Background(), root, serverPath, "./cmd/asteria-server", target.goarch, ldflags); err != nil {
			os.RemoveAll(staging)
			return err
		}
		if err := build(context.Background(), root, migratePath, "./cmd/asteria-migrate", target.goarch, ldflags); err != nil {
			os.RemoveAll(staging)
			return err
		}
		if err := build(context.Background(), root, verifyPath, "./cmd/asteria-verify-storage", target.goarch, ldflags); err != nil {
			os.RemoveAll(staging)
			return err
		}
		server, err := os.ReadFile(serverPath)
		if err != nil {
			os.RemoveAll(staging)
			return fmt.Errorf("read server binary: %w", err)
		}
		migrate, err := os.ReadFile(migratePath)
		if err != nil {
			os.RemoveAll(staging)
			return fmt.Errorf("read migrate binary: %w", err)
		}
		verify, err := os.ReadFile(verifyPath)
		if err != nil {
			os.RemoveAll(staging)
			return fmt.Errorf("read storage verifier binary: %w", err)
		}
		readme, err := os.ReadFile(filepath.Join(root, "README.md"))
		if err != nil {
			os.RemoveAll(staging)
			return fmt.Errorf("read README.md: %w", err)
		}
		name := fmt.Sprintf("asteria-drive_%s_linux_%s.tar.gz", strings.TrimPrefix(version, "v"), target.arch)
		path := filepath.Join(output, name)
		if err := release.WriteArchive(path, []release.ArchiveFile{
			{Name: "README.md", Mode: 0o644, Data: readme},
			{Name: "asteria-migrate", Mode: 0o755, Data: migrate},
			{Name: "asteria-server", Mode: 0o755, Data: server},
			{Name: "asteria-verify-storage", Mode: 0o755, Data: verify},
		}, time.Unix(epoch, 0)); err != nil {
			os.RemoveAll(staging)
			return err
		}
		artifacts = append(artifacts, name)
		os.RemoveAll(staging)
	}
	sort.Strings(artifacts)
	manifest := release.Manifest{Version: version, Commit: commit, SourceDateEpoch: epoch, Artifacts: artifacts}
	if err := release.WriteManifest(filepath.Join(output, "release-manifest.json"), manifest); err != nil {
		return err
	}
	return nil
}

func build(ctx context.Context, root, output, packagePath, goarch, ldflags string) error {
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", output, packagePath)
	command.Dir = root
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build %s for linux/%s: %w", packagePath, goarch, err)
	}
	return nil
}

func repositoryRoot() (string, error) {
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("find repository root: %w", err)
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", fmt.Errorf("repository root is empty")
	}
	return root, nil
}

package release

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ArchiveFile struct {
	Name string
	Mode int64
	Data []byte
}

func WriteArchive(path string, files []ArchiveFile, epoch time.Time) error {
	if path == "" || len(files) == 0 {
		return fmt.Errorf("archive path and files are required")
	}
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file.Name == "" || filepath.Base(file.Name) != file.Name {
			return fmt.Errorf("archive file name must be a plain file name: %q", file.Name)
		}
		if _, ok := seen[file.Name]; ok {
			return fmt.Errorf("duplicate archive file %q", file.Name)
		}
		seen[file.Name] = struct{}{}
		if file.Mode == 0 {
			return fmt.Errorf("archive file %q has no mode", file.Name)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".release-archive-*")
	if err != nil {
		return fmt.Errorf("create archive temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	compressed := gzip.NewWriter(temporary)
	compressed.Header.ModTime = epoch.UTC()
	compressed.Header.OS = 255
	tarWriter := tar.NewWriter(compressed)
	for _, file := range files {
		header := &tar.Header{
			Name:       file.Name,
			Mode:       file.Mode,
			Size:       int64(len(file.Data)),
			ModTime:    epoch.UTC(),
			AccessTime: epoch.UTC(),
			ChangeTime: epoch.UTC(),
			Uid:        0,
			Gid:        0,
			Typeflag:   tar.TypeReg,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("write %s header: %w", file.Name, err)
		}
		if _, err := tarWriter.Write(file.Data); err != nil {
			return fmt.Errorf("write %s: %w", file.Name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("close tar archive: %w", err)
	}
	if err := compressed.Close(); err != nil {
		return fmt.Errorf("close gzip archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close archive file: %w", err)
	}
	if err := os.Chmod(temporaryName, 0o644); err != nil {
		return fmt.Errorf("set archive permissions: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish archive: %w", err)
	}
	return nil
}

func WriteChecksums(directory, outputName string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read release directory: %w", err)
	}
	if outputName == "" {
		outputName = "checksums.txt"
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == outputName {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("release entry %s is not a regular file", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("release directory contains no files")
	}
	var output strings.Builder
	for _, name := range names {
		file, err := os.Open(filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("open %s: %w", name, err)
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("hash %s: %w", name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", name, closeErr)
		}
		output.WriteString(hex.EncodeToString(digest.Sum(nil)))
		output.WriteString("  ")
		output.WriteString(name)
		output.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(directory, outputName), []byte(output.String()), 0o644)
}

type Manifest struct {
	Format          string   `json:"format"`
	Version         string   `json:"version"`
	Commit          string   `json:"commit"`
	SourceDateEpoch int64    `json:"source_date_epoch"`
	Artifacts       []string `json:"artifacts"`
}

func WriteManifest(path string, manifest Manifest) error {
	manifest.Format = "asteria-drive-release/v1"
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode release manifest: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

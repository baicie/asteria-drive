package release

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteArchiveIsDeterministic(t *testing.T) {
	directory := t.TempDir()
	epoch := time.Unix(1_750_000_000, 0).UTC()
	files := []ArchiveFile{
		{Name: "asteria-server", Mode: 0o755, Data: []byte("server")},
		{Name: "README.md", Mode: 0o644, Data: []byte("readme\n")},
	}
	first := filepath.Join(directory, "first.tar.gz")
	second := filepath.Join(directory, "second.tar.gz")
	if err := WriteArchive(first, files, epoch); err != nil {
		t.Fatal(err)
	}
	if err := WriteArchive(second, files, epoch); err != nil {
		t.Fatal(err)
	}
	firstData, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstData) != string(secondData) {
		t.Fatal("archives differ for identical inputs")
	}

	reader, err := gzip.NewReader(strings.NewReader(string(firstData)))
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(reader)
	for _, want := range []struct {
		name string
		mode int64
		body string
	}{
		{name: "README.md", mode: 0o644, body: "readme\n"},
		{name: "asteria-server", mode: 0o755, body: "server"},
	} {
		header, err := tarReader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != want.name || header.Mode != want.mode {
			t.Fatalf("entry = %#v, want %s mode %o", header, want.name, want.mode)
		}
		body, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != want.body {
			t.Fatalf("%s body = %q, want %q", want.name, body, want.body)
		}
	}
	if _, err := tarReader.Next(); err != io.EOF {
		t.Fatalf("archive has unexpected extra entries: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteChecksumsSortsAndExcludesOutput(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "b.tar.gz"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "a.tar.gz"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteChecksums(directory, "checksums.txt"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	a := sha256.Sum256([]byte("a"))
	b := sha256.Sum256([]byte("b"))
	want := hex.EncodeToString(a[:]) + "  a.tar.gz\n" + hex.EncodeToString(b[:]) + "  b.tar.gz\n"
	if string(data) != want {
		t.Fatalf("checksums = %q, want %q", data, want)
	}
}

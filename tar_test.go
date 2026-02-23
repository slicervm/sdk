package slicer

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeExcludePatterns(t *testing.T) {
	got := normalizeExcludePatterns("  ./foo/ ", "/bar/ ", "  baz  ", "", " ./a/b/ ")
	want := []string{"foo", "bar", "baz", "a/b"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeExcludePatterns() = %#v, want %#v", got, want)
	}
}

func TestShouldExcludePath(t *testing.T) {
	excludes := normalizeExcludePatterns("skipme/**", "*.tmp", "nested/*.txt", "a/**/deep")

	if !shouldExcludePath("skipme/file.txt", excludes) {
		t.Fatal("expected path with base directory pattern to be excluded")
	}

	if !shouldExcludePath("other.tmp", excludes) {
		t.Fatal("expected *.tmp pattern to match")
	}

	if !shouldExcludePath("nested/skip.txt", excludes) {
		t.Fatal("expected nested/*.txt pattern to match nested/skip.txt")
	}

	if shouldExcludePath("nested/skip.bin", excludes) {
		t.Fatal("expected nested/*.txt pattern to not match non-txt files")
	}

	if !shouldExcludePath("a/x/deep", excludes) {
		t.Fatal("expected recursive pattern 'a/**/deep' to match")
	}
}

func TestStreamTarArchive_ExcludesPaths(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("failed to create source dir: %v", err)
	}

	keepFile := filepath.Join(sourceDir, "keep.txt")
	skipFile := filepath.Join(sourceDir, "skip.tmp")
	skipDir := filepath.Join(sourceDir, "skipme")
	nestedFile := filepath.Join(skipDir, "inside.txt")
	nestedKeepDir := filepath.Join(sourceDir, "nested")
	nestedKeepFile := filepath.Join(nestedKeepDir, "still-keep.txt")

	if err := os.WriteFile(keepFile, []byte("keep"), 0o644); err != nil {
		t.Fatalf("failed to write keep file: %v", err)
	}
	if err := os.WriteFile(skipFile, []byte("skip"), 0o644); err != nil {
		t.Fatalf("failed to write skip file: %v", err)
	}
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatalf("failed to create skip dir: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("ignore"), 0o644); err != nil {
		t.Fatalf("failed to write nested skipped file: %v", err)
	}
	if err := os.MkdirAll(nestedKeepDir, 0o755); err != nil {
		t.Fatalf("failed to create nested keep dir: %v", err)
	}
	if err := os.WriteFile(nestedKeepFile, []byte("keep-nested"), 0o644); err != nil {
		t.Fatalf("failed to write nested keep file: %v", err)
	}

	ctx := context.Background()
	var buf bytes.Buffer
	if err := StreamTarArchive(ctx, &buf, tmpDir, "source", "*.tmp", "skipme/**"); err != nil {
		t.Fatalf("StreamTarArchive() error = %v", err)
	}

	names := collectTarEntryNames(t, buf.Bytes())

	if _, ok := names["skip.tmp"]; ok {
		t.Error("expected skip.tmp to be excluded")
	}
	if _, ok := names["skipme/"]; ok {
		t.Error("expected skipme directory to be excluded")
	}
	if _, ok := names["skipme/inside.txt"]; ok {
		t.Error("expected file inside skipme to be excluded")
	}
	if _, ok := names["keep.txt"]; !ok {
		t.Error("expected keep.txt to be included")
	}
	if _, ok := names["nested/still-keep.txt"]; !ok {
		t.Error("expected nested/still-keep.txt to be included")
	}
}

func TestExtractTarToPath_RespectsExclusions(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("failed to create source dir: %v", err)
	}

	keepFile := filepath.Join(sourceDir, "keep.txt")
	skipFile := filepath.Join(sourceDir, "skip.tmp")
	skipDir := filepath.Join(sourceDir, "skipme")
	nestedFile := filepath.Join(skipDir, "inside.txt")

	if err := os.WriteFile(keepFile, []byte("keep"), 0o644); err != nil {
		t.Fatalf("failed to write keep file: %v", err)
	}
	if err := os.WriteFile(skipFile, []byte("skip"), 0o644); err != nil {
		t.Fatalf("failed to write skip file: %v", err)
	}
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatalf("failed to create skip dir: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("ignore"), 0o644); err != nil {
		t.Fatalf("failed to write skipped nested file: %v", err)
	}

	var buf bytes.Buffer
	if err := StreamTarArchive(context.Background(), &buf, tmpDir, "source", "*.tmp", "skipme/**"); err != nil {
		t.Fatalf("StreamTarArchive() error = %v", err)
	}

	destDir := filepath.Join(tmpDir, "dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("failed to create dest dir: %v", err)
	}
	if err := ExtractTarToPath(context.Background(), bytes.NewReader(buf.Bytes()), destDir, 0, 0, "*.tmp", "skipme/**"); err != nil {
		t.Fatalf("ExtractTarToPath() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "keep.txt")); err != nil {
		t.Fatalf("expected keep.txt to exist after extraction: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "skip.tmp")); !os.IsNotExist(err) {
		t.Fatal("expected skip.tmp to be excluded from extraction")
	}
	if _, err := os.Stat(filepath.Join(destDir, "skipme")); !os.IsNotExist(err) {
		t.Fatal("expected skipme directory to be excluded from extraction")
	}
}

func collectTarEntryNames(t *testing.T, data []byte) map[string]struct{} {
	t.Helper()

	names := make(map[string]struct{})
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("failed to read tar: %v", err)
		}
		names[header.Name] = struct{}{}
	}
}

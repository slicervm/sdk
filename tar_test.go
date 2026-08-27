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
	"time"
)

func TestNormalizeExcludePatterns(t *testing.T) {
	got := normalizeExcludePatterns("  ./foo/ ", "/bar/ ", "  baz  ", "", " ./a/b/ ")
	want := []string{"foo", "/bar", "baz", "a/b"}

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

func TestShouldExcludePathHonoursRootAnchor(t *testing.T) {
	excludes := normalizeExcludePatterns("/superterm", "/superterm-tui")

	for _, name := range []string{"superterm", "superterm-tui"} {
		if !shouldExcludePath(name, excludes) {
			t.Fatalf("expected root path %q to be excluded", name)
		}
	}

	for _, name := range []string{"internal/superterm", "cmd/superterm-tui"} {
		if shouldExcludePath(name, excludes) {
			t.Fatalf("root-anchored pattern unexpectedly excluded %q", name)
		}
	}

	if !shouldExcludePath("internal/superterm", normalizeExcludePatterns("superterm")) {
		t.Fatal("expected unanchored basename pattern to match at any depth")
	}
}

func TestStreamTarArchiveHonoursRootAnchoredExclude(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "internal", "superterm"), 0o755); err != nil {
		t.Fatalf("create source directories: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "superterm"), []byte("binary"), 0o755); err != nil {
		t.Fatalf("write root binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "internal", "superterm", "main.go"), []byte("package superterm\n"), 0o644); err != nil {
		t.Fatalf("write nested source: %v", err)
	}

	var archive bytes.Buffer
	if err := StreamTarArchive(context.Background(), &archive, root, "source", "/superterm"); err != nil {
		t.Fatalf("StreamTarArchive() error = %v", err)
	}

	names := collectTarEntryNames(t, archive.Bytes())
	if _, ok := names["superterm"]; ok {
		t.Fatal("expected root binary to be excluded")
	}
	if _, ok := names["internal/superterm/main.go"]; !ok {
		t.Fatal("expected nested source file to be included")
	}
}

func TestValidRelPathRejectsRootAliases(t *testing.T) {
	for _, name := range []string{".", "./", "a/..", "a/.", "a/../b", "a//b", "../a"} {
		t.Run(name, func(t *testing.T) {
			if ValidRelPath(name) {
				t.Fatalf("ValidRelPath(%q) = true", name)
			}
		})
	}
	for _, name := range []string{"a", "a/b", `name\\with\\backslashes`} {
		if !ValidRelPath(name) {
			t.Fatalf("ValidRelPath(%q) = false", name)
		}
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

func TestExtractTarStreamRejectsExistingParentSymlink(t *testing.T) {
	root := t.TempDir()
	extractDir := filepath.Join(root, "extract")
	escapeDir := filepath.Join(root, "escape")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatalf("create extract directory: %v", err)
	}
	if err := os.MkdirAll(escapeDir, 0o755); err != nil {
		t.Fatalf("create escape directory: %v", err)
	}
	if err := os.Symlink(escapeDir, filepath.Join(extractDir, "linked")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	contents := []byte("must stay contained")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "linked/escaped.txt",
		Mode:     0o600,
		Size:     int64(len(contents)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatalf("write tar contents: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	err := ExtractTarStream(context.Background(), bytes.NewReader(archive.Bytes()), extractDir, 0, 0)
	if err == nil {
		t.Fatalf("ExtractTarStream() error = %v, want symlink rejection", err)
	}
	if _, err := os.Stat(filepath.Join(escapeDir, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped extraction root: %v", err)
	}
}

func TestExtractTarStreamAllowsSymlinkedExtractionRoot(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	linkDir := filepath.Join(root, "link")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("create real directory: %v", err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("create extraction symlink: %v", err)
	}

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	contents := []byte("contents")
	if err := tw.WriteHeader(&tar.Header{Name: "file.txt", Mode: 0o600, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatalf("write tar contents: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	if err := ExtractTarStream(context.Background(), bytes.NewReader(archive.Bytes()), linkDir, 0, 0); err != nil {
		t.Fatalf("ExtractTarStream() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(realDir, "file.txt"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(got) != string(contents) {
		t.Fatalf("extracted contents = %q, want %q", got, contents)
	}
}

func TestExtractTarStreamAtomicallyReplacesConcurrentSymlink(t *testing.T) {
	extractDir := t.TempDir()
	destination := filepath.Join(extractDir, "file.txt")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatalf("write old destination: %v", err)
	}
	victim := filepath.Join(extractDir, "victim.txt")
	if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	contents := []byte("replacement")
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "file.txt", Mode: 0o600, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatalf("write tar contents: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	r := &tarPayloadHookReader{
		reader: bytes.NewReader(archive.Bytes()),
		hook: func() error {
			if err := os.Remove(destination); err != nil {
				return err
			}
			return os.Symlink("victim.txt", destination)
		},
	}
	if err := ExtractTarStream(context.Background(), r, extractDir, 0, 0); err != nil {
		t.Fatalf("ExtractTarStream() error = %v", err)
	}
	if !r.triggered {
		t.Fatal("payload hook was not triggered")
	}
	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatalf("lstat destination: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("destination symlink was followed or left in place")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(contents) {
		t.Fatalf("destination contents = %q, want %q", got, contents)
	}
	gotVictim, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(gotVictim) != "victim" {
		t.Fatalf("victim contents = %q, want unchanged", gotVictim)
	}
}

type tarPayloadHookReader struct {
	reader    *bytes.Reader
	hook      func() error
	offset    int64
	triggered bool
}

func (r *tarPayloadHookReader) Read(p []byte) (int, error) {
	const tarBlockSize = 512
	if r.offset >= tarBlockSize && !r.triggered {
		r.triggered = true
		if err := r.hook(); err != nil {
			return 0, err
		}
	}
	if remaining := tarBlockSize - r.offset; remaining > 0 && int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.reader.Read(p)
	r.offset += int64(n)
	return n, err
}

func TestExtractTarStreamPreservesModificationTimes(t *testing.T) {
	extractDir := t.TempDir()
	modified := time.Unix(1_700_000_000, 0).UTC()

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "dir", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: modified}); err != nil {
		t.Fatalf("write directory header: %v", err)
	}
	contents := []byte("contents")
	if err := tw.WriteHeader(&tar.Header{Name: "dir/file.txt", Mode: 0o600, Size: int64(len(contents)), Typeflag: tar.TypeReg, ModTime: modified}); err != nil {
		t.Fatalf("write file header: %v", err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatalf("write file contents: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	if err := ExtractTarStream(context.Background(), bytes.NewReader(archive.Bytes()), extractDir, 0, 0); err != nil {
		t.Fatalf("ExtractTarStream() error = %v", err)
	}
	for _, name := range []string{"dir", "dir/file.txt"} {
		info, err := os.Stat(filepath.Join(extractDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if !info.ModTime().Equal(modified) {
			t.Fatalf("mtime for %s = %s, want %s", name, info.ModTime(), modified)
		}
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

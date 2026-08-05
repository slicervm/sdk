package slicer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCopyManifestValidate(t *testing.T) {
	digest := strings.Repeat("a", 64)
	manifest := CopyManifest{
		Version:     ChunkedCopyManifestVersion,
		Mode:        "binary",
		Destination: "/home/ubuntu/tool",
		Size:        3,
		Chunks: []CopyChunk{{
			Index:  0,
			Size:   3,
			SHA256: digest,
		}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}

	badIndex := manifest
	badIndex.Chunks = append([]CopyChunk(nil), manifest.Chunks...)
	badIndex.Chunks[0].Index = 1
	if err := badIndex.Validate(); err == nil {
		t.Fatal("out-of-sequence chunk was accepted")
	}

	badSize := manifest
	badSize.Size++
	if err := badSize.Validate(); err == nil {
		t.Fatal("incorrect stream size was accepted")
	}
}

func TestCopyManifestV2AllowsUnnamedContentsSource(t *testing.T) {
	manifest := CopyManifest{
		Version:       ChunkedCopyManifestV2,
		Mode:          "tar",
		Destination:   "/tmp/root",
		CopySemantics: cpCopySemanticsV1,
		SourceType:    copySourceTypeDir,
		CopyContents:  true,
		Chunks:        []CopyChunk{},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestCopyManifestV1OmitsV2Fields(t *testing.T) {
	data, err := json.Marshal(CopyManifest{
		Version:     ChunkedCopyManifestVersion,
		Mode:        "binary",
		Destination: "/tmp/file",
		Chunks:      []CopyChunk{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"copy_semantics", "source_name", "source_type", "copy_contents"} {
		if strings.Contains(string(data), `"`+field+`"`) {
			t.Fatalf("v1 manifest contains %s: %s", field, data)
		}
	}
}

func TestCopyChunkFileName(t *testing.T) {
	chunk := CopyChunk{Index: 12, SHA256: strings.Repeat("b", 64)}
	want := "000012.chunk"
	if got := CopyChunkFileName(chunk); got != want {
		t.Fatalf("CopyChunkFileName() = %q, want %q", got, want)
	}
}

package slicer

import (
	"encoding/hex"
	"fmt"
)

const (
	ChunkedCopyManifestVersion = 1
	ChunkedCopyManifestV2      = 2
	DefaultCopyChunkSize       = 64 << 20
	DefaultCopyConcurrency     = 4
)

// CopyChunk describes one checksummed part of a chunked copy stream.
type CopyChunk struct {
	Index  int    `json:"index"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// CopyManifest describes the ordered byte stream and its final destination.
// The format is deliberately small so it can be implemented consistently by
// the Go, TypeScript, and Python SDKs.
type CopyManifest struct {
	Version       int         `json:"version"`
	Mode          string      `json:"mode"`
	Destination   string      `json:"destination"`
	UID           uint32      `json:"uid"`
	GID           uint32      `json:"gid"`
	Permissions   string      `json:"permissions,omitempty"`
	Size          int64       `json:"size"`
	UnpackedSize  int64       `json:"unpacked_size,omitempty"`
	CopySemantics string      `json:"copy_semantics,omitempty"`
	SourceName    string      `json:"source_name,omitempty"`
	SourceType    string      `json:"source_type,omitempty"`
	CopyContents  bool        `json:"copy_contents,omitempty"`
	Chunks        []CopyChunk `json:"chunks"`
}

// ChunkedCopyOptions controls a host-to-VM chunked copy.
type ChunkedCopyOptions struct {
	UID             uint32
	GID             uint32
	Permissions     string
	Mode            string
	ExcludePatterns []string
	ChunkSize       int
	Concurrency     int
}

// CopyChunkFileName returns the portable on-guest filename for a chunk.
func CopyChunkFileName(chunk CopyChunk) string {
	return fmt.Sprintf("%06d.chunk", chunk.Index)
}

// Validate checks the portable manifest independently of any filesystem.
func (m CopyManifest) Validate() error {
	if m.Version != ChunkedCopyManifestVersion && m.Version != ChunkedCopyManifestV2 {
		return fmt.Errorf("unsupported copy manifest version: %d", m.Version)
	}
	if m.Version == ChunkedCopyManifestV2 {
		if m.CopySemantics != cpCopySemanticsV1 {
			return fmt.Errorf("invalid copy semantics: %q", m.CopySemantics)
		}
		if m.SourceType != copySourceTypeFile && m.SourceType != copySourceTypeDir {
			return fmt.Errorf("invalid copy source type: %q", m.SourceType)
		}
		if !m.CopyContents && !validCopySourceName(m.SourceName) {
			return fmt.Errorf("invalid copy source name: %q", m.SourceName)
		}
		if m.SourceType == copySourceTypeFile && (m.Mode != "binary" || m.CopyContents) {
			return fmt.Errorf("file copy semantics require binary mode without copy_contents")
		}
		if m.SourceType == copySourceTypeDir && m.Mode != "tar" {
			return fmt.Errorf("directory copy semantics require tar mode")
		}
	}
	if m.Mode != "binary" && m.Mode != "tar" {
		return fmt.Errorf("invalid copy mode: %q", m.Mode)
	}
	if m.Destination == "" {
		return fmt.Errorf("copy destination is required")
	}
	if m.Size < 0 || m.UnpackedSize < 0 {
		return fmt.Errorf("copy sizes must not be negative")
	}
	var total int64
	for i, chunk := range m.Chunks {
		if chunk.Index != i {
			return fmt.Errorf("chunk index %d is out of sequence, expected %d", chunk.Index, i)
		}
		if chunk.Size <= 0 {
			return fmt.Errorf("chunk %d has invalid size %d", i, chunk.Size)
		}
		if !validSHA256(chunk.SHA256) {
			return fmt.Errorf("chunk %d has invalid SHA-256: %q", i, chunk.SHA256)
		}
		if total > m.Size-chunk.Size {
			return fmt.Errorf("chunk sizes exceed declared stream size")
		}
		total += chunk.Size
	}
	if total != m.Size {
		return fmt.Errorf("chunk sizes total %d bytes, expected %d", total, m.Size)
	}
	if m.Size > 0 && len(m.Chunks) == 0 {
		return fmt.Errorf("non-empty copy has no chunks")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

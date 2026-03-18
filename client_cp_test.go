package slicer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareLocalTarDestination(t *testing.T) {
	t.Run("creates missing directory", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "missing")

		got, err := prepareLocalTarDestination(dest)
		if err != nil {
			t.Fatalf("prepareLocalTarDestination() error = %v", err)
		}

		if got != dest {
			t.Fatalf("prepareLocalTarDestination() = %q, want %q", got, dest)
		}

		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("expected destination to exist: %v", err)
		}
		if !info.IsDir() {
			t.Fatal("expected destination to be a directory")
		}
	})

	t.Run("accepts existing directory", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "existing")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatalf("failed to create directory: %v", err)
		}

		got, err := prepareLocalTarDestination(dest)
		if err != nil {
			t.Fatalf("prepareLocalTarDestination() error = %v", err)
		}
		if got != dest {
			t.Fatalf("prepareLocalTarDestination() = %q, want %q", got, dest)
		}
	})

	t.Run("rejects existing file", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(dest, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		if _, err := prepareLocalTarDestination(dest); err == nil {
			t.Fatal("expected error for file destination")
		}
	})
}

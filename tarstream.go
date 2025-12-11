package slicer

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StreamTarArchive streams a tar archive of the given path to w.
// This is a streaming implementation that doesn't buffer entire files in memory.
// parentDir and baseName are used to strip leading paths, making it more like cp.
func StreamTarArchive(ctx context.Context, w io.Writer, parentDir, baseName string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()

	sourcePath := filepath.Join(parentDir, baseName)

	return filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err != nil {
			return err
		}

		// Get relative path from parentDir
		relPath, err := filepath.Rel(parentDir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Skip the parent directory itself, only include contents
		if relPath == "." {
			return nil
		}

		// Normalize path separators for tar
		relPath = filepath.ToSlash(relPath)

		// Get file info
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return fmt.Errorf("failed to read symlink %s: %w", path, err)
			}
		}

		// Create tar header
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return fmt.Errorf("failed to create tar header for %s: %w", path, err)
		}

		// Set the name to just the relative path (strips leading paths)
		header.Name = relPath

		// Write header
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header for %s: %w", path, err)
		}

		// For regular files, stream the contents in chunks
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("failed to open file %s: %w", path, err)
			}

			// Stream file contents in chunks (no buffering of entire file)
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return fmt.Errorf("failed to write file contents for %s: %w", path, err)
			}
		}

		return nil
	})
}

// ExtractTarStream extracts a tar stream from r into extractDir.
// This is a streaming implementation that doesn't buffer entire files in memory.
// If uid or gid are non-zero, files will be chowned to that uid/gid after creation.
func ExtractTarStream(ctx context.Context, r io.Reader, extractDir string, uid, gid uint32) error {
	// Get absolute path of extract directory for proper validation
	absExtractDir, err := filepath.Abs(extractDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path of extract directory: %w", err)
	}
	// Ensure extractDir ends with separator for prefix checking
	absExtractDir = filepath.Clean(absExtractDir) + string(filepath.Separator)

	tr := tar.NewReader(r)
	t0 := time.Now()
	loggedChtimesError := false
	madeDir := make(map[string]bool)

	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// Clean and validate path to prevent directory traversal
		// Remove trailing slash for directories
		name := strings.TrimSuffix(header.Name, "/")
		if !ValidRelPath(name) {
			return fmt.Errorf("tar contained invalid name: %q", header.Name)
		}

		// Clean the path to prevent directory traversal
		rel := filepath.FromSlash(name)
		target := filepath.Join(extractDir, rel)

		// Get absolute path for validation
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for %s: %w", target, err)
		}
		absTarget = filepath.Clean(absTarget)

		// Normalize both paths for comparison (remove trailing separator from extractDir)
		absExtractDirBase := strings.TrimSuffix(absExtractDir, string(filepath.Separator))

		// Ensure target is within extractDir (defense in depth)
		if absTarget != absExtractDirBase && !strings.HasPrefix(absTarget, absExtractDirBase+string(filepath.Separator)) {
			return fmt.Errorf("tar entry path outside extract directory: %s (resolved to %s, extract dir: %s)", header.Name, absTarget, absExtractDirBase)
		}

		// Get file info from header
		info := header.FileInfo()
		mode := info.Mode()

		switch header.Typeflag {
		case tar.TypeDir:
			// Create directory
			if err := os.MkdirAll(target, mode.Perm()); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", target, err)
			}
			// Set ownership if specified (skipped on Windows where Chown is not supported)
			if uid > 0 || gid > 0 {
				if err := os.Chown(target, int(uid), int(gid)); err != nil {
					// On Windows, Chown always fails - this is expected and can be ignored
					log.Printf("warning: failed to set ownership on directory %s: %v", target, err)
				}
			}
			madeDir[target] = true

		case tar.TypeReg, tar.TypeRegA:
			// Create parent directories (track to avoid redundant calls)
			parentDir := filepath.Dir(target)
			if !madeDir[parentDir] {
				if err := os.MkdirAll(parentDir, 0o755); err != nil {
					return fmt.Errorf("failed to create parent directory for %s: %w", target, err)
				}
				madeDir[parentDir] = true
			}

			// Remove existing file/symlink if it exists
			if _, err := os.Lstat(target); err == nil {
				if err := os.Remove(target); err != nil {
					return fmt.Errorf("failed to remove existing file %s: %w", target, err)
				}
			}

			// Create/truncate file
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, mode.Perm())
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", target, err)
			}

			// Copy file contents and verify size
			n, err := io.Copy(f, tr)
			if closeErr := f.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
			if err != nil {
				return fmt.Errorf("failed to write file %s: %w", target, err)
			}
			if header.Size > 0 && n != header.Size {
				return fmt.Errorf("only wrote %d bytes to %s; expected %d", n, target, header.Size)
			}

			// Set file permissions
			if err := os.Chmod(target, mode.Perm()); err != nil {
				// Non-fatal, just log
				log.Printf("warning: failed to set permissions on %s: %v", target, err)
			}

			// Set ownership if specified (skipped on Windows where Chown is not supported)
			if uid > 0 || gid > 0 {
				if err := os.Chown(target, int(uid), int(gid)); err != nil {
					// On Windows, Chown always fails - this is expected and can be ignored
					log.Printf("warning: failed to set ownership on file %s: %v", target, err)
				}
			}

			// Set modification time (clamp future times to prevent clock skew issues)
			modTime := header.ModTime
			if modTime.After(t0) {
				// Clamp modtimes at system time to prevent issues with clock skew
				modTime = t0
			}
			if !modTime.IsZero() {
				if err := os.Chtimes(target, modTime, modTime); err != nil {
					if !loggedChtimesError {
						log.Printf("warning: failed to set times on %s: %v (further Chtimes errors suppressed)", target, err)
						loggedChtimesError = true
					}
				}
			}

		case tar.TypeSymlink:
			// Remove existing file/symlink if it exists
			if _, err := os.Lstat(target); err == nil {
				if err := os.Remove(target); err != nil {
					return fmt.Errorf("failed to remove existing file before creating symlink %s: %w", target, err)
				}
			}
			// Create symlink
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("failed to create symlink %s -> %s: %w", target, header.Linkname, err)
			}
			// Set ownership if specified (chown on symlink affects the symlink itself)
			// Skipped on Windows where Chown is not supported
			if uid > 0 || gid > 0 {
				if err := os.Lchown(target, int(uid), int(gid)); err != nil {
					// On Windows, Lchown always fails - this is expected and can be ignored
					log.Printf("warning: failed to set ownership on symlink %s: %v", target, err)
				}
			}
			// Set modification time on symlink (skip if it fails - symlink target might not exist)
			if !header.ModTime.IsZero() {
				if err := os.Chtimes(target, header.ModTime, header.ModTime); err != nil {
					if !loggedChtimesError {
						// This is non-fatal - symlinks to non-existent targets can't have times set
						loggedChtimesError = true
					}
				}
			}

		case tar.TypeLink:
			// Create hard link
			linkTarget := filepath.Join(extractDir, filepath.Clean(header.Linkname))
			if !strings.HasPrefix(linkTarget, extractDir) {
				return fmt.Errorf("hard link target outside extract directory: %s", header.Linkname)
			}
			// Check if target exists before creating hard link
			if _, err := os.Stat(linkTarget); os.IsNotExist(err) {
				// Target doesn't exist yet, skip hard link creation
				// It will be created when we process that entry
				continue
			}
			// Remove existing file/symlink if it exists
			if _, err := os.Lstat(target); err == nil {
				if err := os.Remove(target); err != nil {
					return fmt.Errorf("failed to remove existing file before creating hard link %s: %w", target, err)
				}
			}
			if err := os.Link(linkTarget, target); err != nil {
				return fmt.Errorf("failed to create hard link %s -> %s: %w", target, header.Linkname, err)
			}
			// Set ownership if specified (skipped on Windows where Chown is not supported)
			if uid > 0 || gid > 0 {
				if err := os.Chown(target, int(uid), int(gid)); err != nil {
					// On Windows, Chown always fails - this is expected and can be ignored
					log.Printf("warning: failed to set ownership on hard link %s: %v", target, err)
				}
			}
			// Set modification time on hard link
			if !header.ModTime.IsZero() {
				if err := os.Chtimes(target, header.ModTime, header.ModTime); err != nil {
					if !loggedChtimesError {
						log.Printf("warning: failed to set times on hard link %s: %v (further Chtimes errors suppressed)", target, err)
						loggedChtimesError = true
					}
				}
			}

		default:
			log.Printf("warning: unsupported tar entry type %c for %s", header.Typeflag, header.Name)
			continue
		}
	}

	return nil
}

// ValidRelPath validates that a path is a valid relative path
// and doesn't contain directory traversal attempts
func ValidRelPath(p string) bool {
	if p == "" || strings.Contains(p, `\`) || strings.HasPrefix(p, "/") || strings.Contains(p, "../") {
		return false
	}
	return true
}

// ExtractTarToPath extracts a tar stream to a local path with proper renaming logic.
// This handles the cp-like behavior of renaming files/directories and moving contents
// into existing directories.
// If uid or gid are non-zero, files will be chowned to that uid/gid after creation.
func ExtractTarToPath(ctx context.Context, r io.Reader, dest string, uid, gid uint32) error {
	// Check if destination exists and what type it is
	destInfo, err := os.Stat(dest)
	destExists := err == nil
	destIsDir := destExists && destInfo.IsDir()

	// If destination doesn't exist, check if parent directory exists
	// to determine if user wants a file or directory
	var extractDir string
	var finalDest string
	var needsRename bool

	if destExists {
		if destIsDir {
			// Destination is a directory, but we still need to extract to temp first
			// because the tar contains entries like "etc/mtab" and we don't want
			// to create "dest/etc/mtab" but rather "dest/mtab"
			// So extract to temp, then move contents
			extractDir = filepath.Dir(dest)
			finalDest = dest
			needsRename = true
		} else {
			// Destination is a file, extract to temp location then rename
			extractDir = filepath.Dir(dest)
			finalDest = dest
			needsRename = true
		}
	} else {
		// Destination doesn't exist - this supports renaming
		// e.g., "cp vm:/home/ubuntu/slicer ./slicer-bak" will rename to slicer-bak
		// e.g., "cp vm:/home/ubuntu/go/src/ ./vm-a-go-src" will rename to vm-a-go-src
		parentDir := filepath.Dir(dest)
		parentInfo, err := os.Stat(parentDir)
		if err != nil {
			return fmt.Errorf("parent directory does not exist: %w", err)
		}
		if !parentInfo.IsDir() {
			return fmt.Errorf("parent path is not a directory: %s", parentDir)
		}

		// We'll extract to temp directory and see what comes out
		// If tar contains a single file/dir, we'll rename it to the destination
		extractDir = parentDir
		finalDest = dest
		needsRename = true
	}

	// Create temp directory for extraction if we need to rename
	var tempDir string
	if needsRename {
		tempDir, err = os.MkdirTemp(extractDir, ".slicer-cp-")
		if err != nil {
			return fmt.Errorf("failed to create temp directory: %w", err)
		}
		defer os.RemoveAll(tempDir)
		extractDir = tempDir
	}

	// Extract tar stream using Go's archive/tar package
	if err := ExtractTarStream(ctx, r, extractDir, uid, gid); err != nil {
		return fmt.Errorf("failed to extract tar: %w", err)
	}

	// If we need to rename, find what was extracted and move it
	if needsRename {
		entries, err := os.ReadDir(extractDir)
		if err != nil {
			return fmt.Errorf("failed to read extracted directory: %w", err)
		}

		if len(entries) == 0 {
			return fmt.Errorf("tar archive was empty")
		}

		// Check if destination exists and is a directory
		destInfo, err := os.Stat(finalDest)
		destExistsAsDir := err == nil && destInfo.IsDir()

		if len(entries) > 1 {
			// Multiple entries extracted
			if destExists && !destIsDir {
				return fmt.Errorf("cannot extract multiple files to a single file destination")
			}
			// If destination exists as a directory, move contents into it
			if destExistsAsDir {
				for _, entry := range entries {
					srcPath := filepath.Join(extractDir, entry.Name())
					dstPath := filepath.Join(finalDest, entry.Name())
					// Remove destination if it exists
					if _, err := os.Lstat(dstPath); err == nil {
						if err := os.RemoveAll(dstPath); err != nil {
							return fmt.Errorf("failed to remove existing %s: %w", dstPath, err)
						}
					}
					if err := os.Rename(srcPath, dstPath); err != nil {
						return fmt.Errorf("failed to move %s to %s: %w", entry.Name(), finalDest, err)
					}
				}
				return nil
			}
			// If destination doesn't exist but we have multiple files,
			// treat it as a directory
			if !destExists {
				if err := os.MkdirAll(finalDest, 0o755); err != nil {
					return fmt.Errorf("failed to create destination directory: %w", err)
				}
				// Move all entries to the destination
				for _, entry := range entries {
					srcPath := filepath.Join(extractDir, entry.Name())
					dstPath := filepath.Join(finalDest, entry.Name())
					if err := os.Rename(srcPath, dstPath); err != nil {
						return fmt.Errorf("failed to move %s to %s: %w", entry.Name(), finalDest, err)
					}
				}
				return nil
			}
		}

		// Single entry extracted
		extractedPath := filepath.Join(extractDir, entries[0].Name())

		// If destination exists as a directory, move contents into it
		if destExistsAsDir {
			// Move all contents of the extracted directory into the destination
			extractedEntries, err := os.ReadDir(extractedPath)
			if err != nil {
				// Not a directory, just move the file
				dstPath := filepath.Join(finalDest, entries[0].Name())
				if err := os.Rename(extractedPath, dstPath); err != nil {
					return fmt.Errorf("failed to move %s into %s: %w", entries[0].Name(), finalDest, err)
				}
				return nil
			}
			// It's a directory, move its contents
			for _, entry := range extractedEntries {
				srcPath := filepath.Join(extractedPath, entry.Name())
				dstPath := filepath.Join(finalDest, entry.Name())
				// Remove destination if it exists
				if _, err := os.Lstat(dstPath); err == nil {
					if err := os.RemoveAll(dstPath); err != nil {
						return fmt.Errorf("failed to remove existing %s: %w", dstPath, err)
					}
				}
				if err := os.Rename(srcPath, dstPath); err != nil {
					return fmt.Errorf("failed to move %s into %s: %w", entry.Name(), finalDest, err)
				}
			}
			// Remove the now-empty extracted directory
			os.Remove(extractedPath)
			return nil
		}

		// Remove destination if it exists (for file case)
		if destExists && !destIsDir {
			if err := os.Remove(finalDest); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove existing destination: %w", err)
			}
		}

		// Ensure parent directory exists
		parentDir := filepath.Dir(finalDest)
		if err := os.MkdirAll(parentDir, 0o755); err != nil {
			return fmt.Errorf("failed to create parent directory: %w", err)
		}

		// Move extracted file/dir to final destination
		if err := os.Rename(extractedPath, finalDest); err != nil {
			return fmt.Errorf("failed to move extracted content to destination: %w", err)
		}
	}

	return nil
}


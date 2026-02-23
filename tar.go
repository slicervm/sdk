package slicer

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// StreamTarArchive streams a tar archive of regular files and directories to w.
// Only handles regular files and directories. Preserves mtime and executable bit.
// Skips symlinks, devices, and other special files.
func StreamTarArchive(ctx context.Context, w io.Writer, parentDir, baseName string, excludePatterns ...string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()

	sourcePath := filepath.Join(parentDir, baseName)
	excludes := normalizeExcludePatterns(excludePatterns...)

	return filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err != nil {
			return err
		}

		// Skip non-regular files and non-directories
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}

		// Make paths relative to sourcePath (not parentDir) so that copying /etc
		// creates entries like "passwd" not "etc/passwd"
		relPath, err := filepath.Rel(sourcePath, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Skip the source directory itself
		if relPath == "." {
			return nil
		}

		relPath = filepath.ToSlash(relPath)
		if shouldExcludePath(relPath, excludes) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Create header with normalized permissions (strip setuid/setgid/sticky)
		mode := info.Mode().Perm()
		if info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			// Preserve executable bit
			mode |= 0111
		}

		header := &tar.Header{
			Name:    relPath,
			Size:    info.Size(),
			Mode:    int64(mode),
			ModTime: info.ModTime(),
		}

		if info.IsDir() {
			header.Typeflag = tar.TypeDir
			header.Name += "/"
		} else {
			header.Typeflag = tar.TypeReg
		}

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header for %s: %w", path, err)
		}

		// Stream file contents
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("failed to open file %s: %w", path, err)
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return fmt.Errorf("failed to write file contents for %s: %w", path, err)
			}
		}

		return nil
	})
}

func shouldExcludePath(relPath string, excludes []string) bool {
	if relPath == "" || len(excludes) == 0 {
		return false
	}

	normPath := filepath.ToSlash(relPath)
	baseName := filepath.Base(normPath)

	for _, pattern := range excludes {
		if pattern == "" {
			continue
		}

		if matchPattern(pattern, normPath) {
			return true
		}

		if !strings.Contains(pattern, "/") {
			match, err := path.Match(pattern, baseName)
			if err != nil {
				continue
			}
			if match {
				return true
			}
		}
	}

	return false
}

func normalizeExcludePatterns(patterns ...string) []string {
	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		pattern = strings.TrimPrefix(pattern, "./")
		pattern = strings.TrimPrefix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")

		if pattern != "" {
			normalized = append(normalized, pattern)
		}
	}
	return normalized
}

func matchPattern(pattern string, candidate string) bool {
	if pattern == "" {
		return false
	}

	pattern = strings.Trim(pattern, "/")
	candidate = strings.Trim(candidate, "/")
	if pattern == "" {
		return candidate == ""
	}

	patternSegments := splitPattern(pattern)
	candidateSegments := splitPattern(candidate)
	if len(patternSegments) == 0 || len(candidateSegments) == 0 {
		return false
	}

	return matchPatternSegments(patternSegments, candidateSegments, 0, 0)
}

func matchPatternSegments(patterns, paths []string, patternIdx, pathIdx int) bool {
	if patternIdx == len(patterns) {
		return pathIdx == len(paths)
	}

	pattern := patterns[patternIdx]
	if pattern == "**" {
		for i := pathIdx; i <= len(paths); i++ {
			if matchPatternSegments(patterns, paths, patternIdx+1, i) {
				return true
			}
		}
		return false
	}

	if pathIdx >= len(paths) {
		return false
	}

	match, err := path.Match(pattern, paths[pathIdx])
	if err != nil {
		return false
	}

	if !match {
		return false
	}

	return matchPatternSegments(patterns, paths, patternIdx+1, pathIdx+1)
}

func splitPattern(input string) []string {
	input = strings.Trim(input, "/")
	if input == "" {
		return nil
	}

	return strings.Split(filepath.ToSlash(input), "/")
}

// ExtractTarStream extracts a tar stream from r into extractDir.
// Only handles regular files and directories. Preserves mtime and executable bit.
// Normalizes permissions (strips setuid/setgid/sticky bits). Skips all other entry types.
// If uid or gid are non-zero, files will be chowned to that uid/gid after creation.
// Note: Permissions are set when opening files (efficient), chown is only applied if uid/gid are non-zero.
func ExtractTarStream(ctx context.Context, r io.Reader, extractDir string, uid, gid uint32, excludePatterns ...string) error {
	excludes := normalizeExcludePatterns(excludePatterns...)

	absExtractDir, err := filepath.Abs(extractDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path of extract directory: %w", err)
	}
	absExtractDir = filepath.Clean(absExtractDir) + string(filepath.Separator)

	tr := tar.NewReader(r)
	madeDir := make(map[string]bool)

	for {
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

		// Validate path
		name := strings.TrimSuffix(header.Name, "/")
		if !ValidRelPath(name) {
			return fmt.Errorf("tar contained invalid name: %q", header.Name)
		}

		rel := filepath.FromSlash(name)
		relPattern := filepath.ToSlash(rel)
		if shouldExcludePath(relPattern, excludes) {
			continue
		}
		target := filepath.Join(extractDir, rel)

		// Security: ensure target is within extractDir
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for %s: %w", target, err)
		}
		absTarget = filepath.Clean(absTarget)
		absExtractDirBase := strings.TrimSuffix(absExtractDir, string(filepath.Separator))
		if absTarget != absExtractDirBase && !strings.HasPrefix(absTarget, absExtractDirBase+string(filepath.Separator)) {
			return fmt.Errorf("tar entry path outside extract directory: %s", header.Name)
		}

		// Normalize permissions (strip setuid/setgid/sticky, preserve executable)
		// Note: .Perm() already masks to valid permission bits (0-0777), no range validation needed
		mode := os.FileMode(header.Mode).Perm()
		if header.Mode&0111 != 0 {
			mode |= 0111
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", target, err)
			}
			madeDir[target] = true
			// Set ownership if requested (only on Linux, skipped on Windows)
			// Note: We don't validate uid/gid ranges - the OS will reject invalid values
			if uid > 0 || gid > 0 {
				os.Chown(target, int(uid), int(gid)) // Error ignored for Windows compatibility
			}
			// Preserve mtime
			if !header.ModTime.IsZero() {
				os.Chtimes(target, header.ModTime, header.ModTime)
			}

		case tar.TypeReg, tar.TypeRegA:
			// Create parent directories
			parentDir := filepath.Dir(target)
			if !madeDir[parentDir] {
				if err := os.MkdirAll(parentDir, 0o755); err != nil {
					return fmt.Errorf("failed to create parent directory for %s: %w", target, err)
				}
				madeDir[parentDir] = true
			}

			// Remove existing file if it exists
			os.Remove(target)

			// Create and write file
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", target, err)
			}

			n, err := io.Copy(f, tr)
			closeErr := f.Close()
			if err != nil {
				return fmt.Errorf("failed to write file %s: %w", target, err)
			}
			if closeErr != nil {
				return fmt.Errorf("failed to close file %s: %w", target, closeErr)
			}
			if header.Size > 0 && n != header.Size {
				return fmt.Errorf("only wrote %d bytes to %s; expected %d", n, target, header.Size)
			}

			// Set permissions (in case umask modified them)
			// Note: Permissions are already set when opening the file, this ensures umask didn't modify them
			os.Chmod(target, mode)

			// Set ownership if requested (only on Linux, skipped on Windows)
			// Note: We only chown if explicitly requested (uid/gid != 0) to avoid overhead on large archives
			// Note: We don't validate uid/gid ranges - the OS will reject invalid values
			if uid > 0 || gid > 0 {
				os.Chown(target, int(uid), int(gid)) // Error ignored for Windows compatibility
			}

			// Preserve mtime
			if !header.ModTime.IsZero() {
				os.Chtimes(target, header.ModTime, header.ModTime)
			}

		default:
			// Skip unsupported types (symlinks, hard links, devices, etc.)
			continue
		}
	}

	return nil
}

// ValidRelPath validates that a path is a valid relative path
// and doesn't contain directory traversal attempts.
// Note: Backslashes are allowed in filenames (e.g., systemd unit files with escaped characters).
// Since tar paths use forward slashes as separators (via filepath.ToSlash()), any backslashes
// in the path are part of the filename, not path separators.
func ValidRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "../") {
		return false
	}
	// Backslashes are allowed because they're part of filenames, not path separators.
	// Path separators are already normalized to forward slashes during archive creation.
	return true
}

// ExtractTarToPath extracts a tar stream to a local path with cp-like renaming.
// If dest exists and is a directory, extracts into it. Otherwise extracts and renames.
// No temporary directories are used - extraction happens directly.
// If uid or gid are non-zero, files will be chowned to that uid/gid after creation.
func ExtractTarToPath(ctx context.Context, r io.Reader, dest string, uid, gid uint32, excludePatterns ...string) error {
	destInfo, err := os.Stat(dest)
	destExists := err == nil
	destIsDir := destExists && destInfo.IsDir()

	var extractDir string
	var topLevelName string

	if destIsDir {
		// Extract directly into the directory
		extractDir = dest
	} else {
		// Extract to parent directory, then rename top-level item to dest
		parentDir := filepath.Dir(dest)
		if _, err := os.Stat(parentDir); err != nil {
			return fmt.Errorf("parent directory does not exist: %w", err)
		}
		extractDir = parentDir
		topLevelName = filepath.Base(dest)
	}

	// Extract directly to extractDir
	if err := ExtractTarStream(ctx, r, extractDir, uid, gid, excludePatterns...); err != nil {
		return fmt.Errorf("failed to extract tar: %w", err)
	}

	// If we need to rename, find the top-level item and rename it
	if topLevelName != "" {
		entries, err := os.ReadDir(extractDir)
		if err != nil {
			return fmt.Errorf("failed to read extracted directory: %w", err)
		}

		if len(entries) == 0 {
			return fmt.Errorf("tar archive was empty")
		}

		if len(entries) > 1 {
			return fmt.Errorf("cannot extract multiple files to single file destination")
		}

		extractedPath := filepath.Join(extractDir, entries[0].Name())
		finalDest := dest

		// Remove destination if it exists
		os.Remove(finalDest)

		// Ensure parent exists (should already, but be safe)
		if err := os.MkdirAll(filepath.Dir(finalDest), 0o755); err != nil {
			return fmt.Errorf("failed to create parent directory: %w", err)
		}

		// Rename to final destination
		if err := os.Rename(extractedPath, finalDest); err != nil {
			return fmt.Errorf("failed to rename extracted content to destination: %w", err)
		}
	}

	return nil
}

package slicer

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// StreamTarArchive streams a tar archive of regular files and directories to w.
// Only handles regular files and directories. Preserves mtime and executable bit.
// Skips symlinks, devices, and other special files.
func StreamTarArchive(ctx context.Context, w io.Writer, parentDir, baseName string, excludePatterns ...string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()

	sourcePath := filepath.Join(parentDir, baseName)
	excludes := normalizeExcludePatterns(excludePatterns...)

	return filepath.Walk(sourcePath, func(path string, info os.FileInfo, walkErr error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Make paths relative to sourcePath (not parentDir) so that copying /etc
		// creates entries like "passwd" not "etc/passwd"
		relPath, relErr := filepath.Rel(sourcePath, path)
		if relErr != nil {
			return fmt.Errorf("failed to get relative path: %w", relErr)
		}

		// Skip the source directory itself
		if relPath == "." {
			return nil
		}

		relPath = filepath.ToSlash(relPath)
		if shouldExcludePath(relPath, excludes) {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if walkErr != nil {
			return walkErr
		}

		// Skip non-regular files and non-directories
		if !info.Mode().IsRegular() && !info.IsDir() {
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

// EstimateTarUnpackedSize returns the total regular-file byte size that
// StreamTarArchive would include for the same source and exclude set.
func EstimateTarUnpackedSize(ctx context.Context, parentDir, baseName string, excludePatterns ...string) (int64, error) {
	sourcePath := filepath.Join(parentDir, baseName)
	excludes := normalizeExcludePatterns(excludePatterns...)
	var total int64

	err := filepath.Walk(sourcePath, func(path string, info os.FileInfo, walkErr error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		relPath, relErr := filepath.Rel(sourcePath, path)
		if relErr != nil {
			return fmt.Errorf("failed to get relative path: %w", relErr)
		}
		if relPath == "." {
			return nil
		}

		relPath = filepath.ToSlash(relPath)
		if shouldExcludePath(relPath, excludes) {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if walkErr != nil {
			return walkErr
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
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
	root, err := os.OpenRoot(absExtractDir)
	if err != nil {
		return fmt.Errorf("failed to open extract directory: %w", err)
	}
	defer root.Close()
	absExtractDir = filepath.Clean(absExtractDir) + string(filepath.Separator)

	tr := tar.NewReader(r)
	madeDir := make(map[string]bool)
	directoryTimes := make(map[string]time.Time)

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
			if err := root.MkdirAll(rel, mode); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", target, err)
			}
			madeDir[rel] = true
			dir, err := root.Open(rel)
			if err != nil {
				return fmt.Errorf("failed to open directory %s: %w", target, err)
			}
			if err := dir.Chmod(mode); err != nil {
				_ = dir.Close()
				return fmt.Errorf("failed to set directory permissions %s: %w", target, err)
			}
			if uid > 0 || gid > 0 {
				_ = dir.Chown(int(uid), int(gid))
			}
			if !header.ModTime.IsZero() {
				directoryTimes[rel] = header.ModTime
			}
			if err := dir.Close(); err != nil {
				return fmt.Errorf("failed to close directory %s: %w", target, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			// Create parent directories
			parentRel := filepath.Dir(rel)
			if !madeDir[parentRel] {
				if err := root.MkdirAll(parentRel, 0o755); err != nil {
					return fmt.Errorf("failed to create parent directory for %s: %w", target, err)
				}
				madeDir[parentRel] = true
			}

			if err := extractTarRegularFile(tr, root, rel, target, header, mode, uid, gid); err != nil {
				return err
			}

		default:
			// Skip unsupported types (symlinks, hard links, devices, etc.)
			continue
		}
	}

	for rel, modified := range directoryTimes {
		dir, err := root.Open(rel)
		if err != nil {
			return fmt.Errorf("failed to reopen directory %s: %w", rel, err)
		}
		if err := setOpenFileTimes(dir, root, rel, modified); err != nil {
			_ = dir.Close()
			return fmt.Errorf("failed to set directory times %s: %w", rel, err)
		}
		if err := dir.Close(); err != nil {
			return fmt.Errorf("failed to close directory %s: %w", rel, err)
		}
	}

	return nil
}

func extractTarRegularFile(r io.Reader, root *os.Root, rel, target string, header *tar.Header, mode os.FileMode, uid, gid uint32) (retErr error) {
	parent, err := root.OpenRoot(filepath.Dir(rel))
	if err != nil {
		return fmt.Errorf("failed to open parent directory for %s: %w", target, err)
	}
	defer func() {
		if err := parent.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("failed to close parent directory for %s: %w", target, err)
		}
	}()

	f, tempName, err := createTarTempFile(parent)
	if err != nil {
		return fmt.Errorf("failed to create temporary file for %s: %w", target, err)
	}
	installed := false
	defer func() {
		if !installed {
			_ = parent.Remove(tempName)
		}
	}()

	n, err := io.Copy(f, r)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to write file %s: %w", target, err)
	}
	if header.Size > 0 && n != header.Size {
		_ = f.Close()
		return fmt.Errorf("only wrote %d bytes to %s; expected %d", n, target, header.Size)
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to set file permissions %s: %w", target, err)
	}
	if uid > 0 || gid > 0 {
		_ = f.Chown(int(uid), int(gid))
	}
	if !header.ModTime.IsZero() {
		if err := setOpenFileTimes(f, parent, tempName, header.ModTime); err != nil {
			_ = f.Close()
			return fmt.Errorf("failed to set file times %s: %w", target, err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close file %s: %w", target, err)
	}
	if err := parent.Rename(tempName, filepath.Base(rel)); err != nil {
		return fmt.Errorf("failed to install file %s: %w", target, err)
	}
	installed = true

	return nil
}

func createTarTempFile(parent *os.Root) (*os.File, string, error) {
	for range 100 {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, "", err
		}
		name := fmt.Sprintf(".slicer-extract-%x", suffix)
		f, err := parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if os.IsExist(err) {
			continue
		}
		return f, name, err
	}
	return nil, "", fmt.Errorf("failed to allocate a unique temporary filename")
}

// ValidRelPath validates that a path is a valid relative path
// and doesn't contain directory traversal attempts.
// Note: Backslashes are allowed in filenames (e.g., systemd unit files with escaped characters).
// Since tar paths use forward slashes as separators (via filepath.ToSlash()), any backslashes
// in the path are part of the filename, not path separators.
func ValidRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	if filepath.Separator == '\\' && strings.Contains(p, `\`) {
		return false
	}
	normalized := filepath.ToSlash(p)
	if path.Clean(normalized) != normalized {
		return false
	}
	for _, component := range strings.Split(normalized, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
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

package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// zipEntry is an archive member whose name has been validated as a
// relative, slash-separated path without "." or ".." components.
type zipEntry struct {
	file      *zip.File
	name      string
	directory bool
}

// safeZipEntryName validates one archive member; errors wrap errKind.
func safeZipEntryName(name string, mode os.FileMode, errKind error) (string, bool, error) {
	directory := strings.HasSuffix(name, "/")
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || strings.Contains(name, `\`) || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", false, fmt.Errorf("%w: package entry %q", errKind, name)
	}
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) {
			return "", false, fmt.Errorf("%w: package entry %q", errKind, name)
		}
	}
	if directory && !mode.IsDir() || !directory && !mode.IsRegular() {
		return "", false, fmt.Errorf("%w: package entry %q", errKind, name)
	}
	return trimmed, directory, nil
}

// extractZipEntries writes validated entries below stage, refusing to follow
// or overwrite anything already there.
func extractZipEntries(ctx context.Context, stage string, entries []zipEntry) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.directory {
			if err := makeStageDir(stage, entry.name); err != nil {
				return err
			}
			continue
		}
		if err := makeStageDir(stage, filepath.ToSlash(filepath.Dir(entry.name))); err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(stage, filepath.FromSlash(entry.name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		in, err := entry.file.Open()
		if err == nil {
			err = copyZipEntry(ctx, out, in, entry.file.UncompressedSize64)
			closeErr := in.Close()
			if err == nil {
				err = closeErr
			}
		}
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func makeStageDir(root, name string) error {
	path := root
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == "." || part == "" {
			continue
		}
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(path)
		}
		if err != nil || info.Mode()&os.ModeType != os.ModeDir {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: staging directory %s", ErrUnsafeSavePath, path)
		}
	}
	return nil
}

func copyZipEntry(ctx context.Context, dst io.Writer, src io.Reader, limit uint64) error {
	buf := make([]byte, 32*1024)
	var copied uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if uint64(n) > limit-copied {
				return fmt.Errorf("%w: package entry exceeds declared size", ErrUnsafeSavePath)
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			copied += uint64(n)
		}
		if readErr == io.EOF {
			if copied != limit {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

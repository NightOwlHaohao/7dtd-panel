package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrServerMustBeStopped = errors.New("server must be stopped before creating a backup")

type BackupInfo struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
	Auto    bool      `json:"auto"`
	Live    bool      `json:"live,omitempty"`
}

type BackupService struct {
	paths  Paths
	server *ServerManager
}

// CreateUserData backs up userdata while the game is stopped.
func (b BackupService) CreateUserData(ctx context.Context) (BackupInfo, error) {
	return b.Create(ctx, false, nil)
}

// Create writes a ZIP of userdata. With the game stopped it holds the
// maintenance lease; while it runs, and only when saveWorld is given, it
// first asks the game to save the world and then copies the live files
// ("hot" backup). auto marks scheduled backups, which retention may prune.
func (b BackupService) Create(ctx context.Context, auto bool, saveWorld func(context.Context) error) (BackupInfo, error) {
	if b.server == nil {
		return BackupInfo{}, ErrServerMustBeStopped
	}
	live := false
	release, err := b.server.BeginStoppedOperation()
	if errors.Is(err, ErrServerRunning) && saveWorld != nil && b.server.Status().State == ServerRunning {
		// Blocks restores, updates and other stopped-only work until done.
		release, err = b.server.BeginConfigMutation()
		live = true
	}
	if errors.Is(err, ErrServerRunning) {
		return BackupInfo{}, ErrServerMustBeStopped
	}
	if err != nil {
		return BackupInfo{}, err
	}
	defer release()
	if live {
		if err := saveWorld(ctx); err != nil {
			return BackupInfo{}, fmt.Errorf("saveworld before backup: %w", err)
		}
		if !waitContext(ctx, liveBackupSettle) {
			return BackupInfo{}, ctx.Err()
		}
	}
	prefix := manualBackupPrefix
	if auto {
		prefix = autoBackupPrefix
	}
	info, err := b.writeUserData(ctx, prefix)
	info.Live = live
	return info, err
}

const (
	manualBackupPrefix = "userdata-"
	autoBackupPrefix   = "userdata-auto-"
)

// liveBackupSettle gives the game time to finish writing after saveworld.
var liveBackupSettle = 3 * time.Second

func (b BackupService) writeUserData(ctx context.Context, prefix string) (BackupInfo, error) {
	if err := os.MkdirAll(b.paths.UserDataBackups, 0700); err != nil {
		return BackupInfo{}, err
	}
	created := time.Now()
	name := uniqueBackupName(b.paths.UserDataBackups, prefix+created.Format("20060102-150405"))
	tmp := filepath.Join(b.paths.UserDataBackups, name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return BackupInfo{}, err
	}
	defer func() { _ = os.Remove(tmp) }()
	stopOutput := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stopOutput()
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		return BackupInfo{}, err
	}
	w := zip.NewWriter(f)
	err = filepath.WalkDir(b.paths.UserData, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == b.paths.UserData {
			return nil
		}
		name, err := zipEntryName(b.paths.UserData, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = name + "/"
			_, err = w.CreateHeader(header)
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name, header.Method = name, zip.Deflate
		out, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		copyErr := copyWithContext(ctx, out, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return ctx.Err()
	})
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	stopOutput()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return BackupInfo{}, ctxErr
	}
	if err != nil {
		return BackupInfo{}, err
	}
	final := filepath.Join(b.paths.UserDataBackups, name)
	if err := os.Rename(tmp, final); err != nil {
		return BackupInfo{}, err
	}
	info, err := os.Stat(final)
	if err != nil {
		return BackupInfo{}, err
	}
	return BackupInfo{Name: name, Created: created, Size: info.Size(), Auto: prefix == autoBackupPrefix}, nil
}

// uniqueBackupName never reuses an existing file name, so two backups made
// within the same second cannot overwrite each other.
func uniqueBackupName(dir, base string) string {
	name := base + ".zip"
	for n := 2; exists(filepath.Join(dir, name)) || exists(filepath.Join(dir, name+".tmp")); n++ {
		name = fmt.Sprintf("%s-%d.zip", base, n)
	}
	return name
}

func (b BackupService) List() ([]BackupInfo, error) {
	entries, err := os.ReadDir(b.paths.UserDataBackups)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	backups := make([]BackupInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		backups = append(backups, BackupInfo{Name: entry.Name(), Created: info.ModTime(), Size: info.Size(), Auto: strings.HasPrefix(entry.Name(), autoBackupPrefix)})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].Name > backups[j].Name })
	return backups, nil
}

func zipEntryName(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return "", errors.New("unsafe backup path")
	}
	name := filepath.ToSlash(rel)
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("unsafe backup path")
		}
	}
	return name, nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src *os.File) error {
	stop := context.AfterFunc(ctx, func() { _ = src.Close() })
	defer stop()
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			for written := 0; written < n; {
				if err := ctx.Err(); err != nil {
					return err
				}
				count, err := dst.Write(buf[written:n])
				if err != nil {
					return err
				}
				if count == 0 {
					return io.ErrShortWrite
				}
				written += count
			}
		}
		if readErr == io.EOF {
			return ctx.Err()
		}
		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return readErr
		}
	}
}

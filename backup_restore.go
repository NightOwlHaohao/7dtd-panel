package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidBackup  = errors.New("invalid backup")
	ErrBackupNotFound = errors.New("backup not found")
)

// keptRestoreSnapshots is how many replaced userdata folders a restore keeps.
const keptRestoreSnapshots = 3

// RestoreResult tells where the replaced userdata folder was kept.
type RestoreResult struct {
	Backup   string `json:"backup"`
	Previous string `json:"previous,omitempty"`
}

// validBackupName accepts only a plain file name the panel could have made.
func validBackupName(name string) bool {
	return name != "" && name == filepath.Base(name) && !strings.ContainsAny(name, `/\:`) &&
		strings.HasPrefix(name, manualBackupPrefix) && strings.HasSuffix(name, ".zip")
}

func (b BackupService) backupPath(name string) (string, error) {
	if !validBackupName(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidBackup, name)
	}
	path := filepath.Join(b.paths.UserDataBackups, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrBackupNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a file", ErrInvalidBackup, name)
	}
	return path, nil
}

// Delete removes one backup ZIP.
func (b BackupService) Delete(name string) error {
	path, err := b.backupPath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// PruneAuto deletes the oldest scheduled backups so at most keep remain.
// Backups made by hand are never pruned.
func (b BackupService) PruneAuto(keep int) ([]string, error) {
	if keep < 1 {
		return nil, nil
	}
	list, err := b.List()
	if err != nil {
		return nil, err
	}
	var removed []string
	kept := 0
	for _, backup := range list { // newest first
		if !backup.Auto {
			continue
		}
		if kept < keep {
			kept++
			continue
		}
		if err := b.Delete(backup.Name); err != nil {
			return removed, err
		}
		removed = append(removed, backup.Name)
	}
	return removed, nil
}

// Restore replaces userdata with the contents of a backup. The game must be
// stopped. The current userdata folder is not deleted: it is moved to
// backups\before-restore so a wrong restore can be undone by hand.
func (b BackupService) Restore(ctx context.Context, name string) (RestoreResult, error) {
	if b.server == nil {
		return RestoreResult{}, ErrServerMustBeStopped
	}
	path, err := b.backupPath(name)
	if err != nil {
		return RestoreResult{}, err
	}
	release, err := b.server.BeginStoppedOperation()
	if errors.Is(err, ErrServerRunning) {
		return RestoreResult{}, ErrServerMustBeStopped
	}
	if err != nil {
		return RestoreResult{}, err
	}
	defer release()

	archive, err := zip.OpenReader(path)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("%w: %v", ErrInvalidBackup, err)
	}
	defer archive.Close()
	entries := make([]zipEntry, 0, len(archive.File))
	files := 0
	for _, file := range archive.File {
		entryName, directory, err := safeZipEntryName(file.Name, file.Mode(), ErrInvalidBackup)
		if err != nil {
			return RestoreResult{}, err
		}
		if !directory {
			files++
		}
		entries = append(entries, zipEntry{file: file, name: entryName, directory: directory})
	}
	if files == 0 {
		return RestoreResult{}, fmt.Errorf("%w: the backup contains no files", ErrInvalidBackup)
	}

	stamp := time.Now().Format("20060102-150405")
	// The stage sits next to userdata so the final swap is a rename.
	stage, err := os.MkdirTemp(filepath.Dir(b.paths.UserData), ".userdata-restore-")
	if err != nil {
		return RestoreResult{}, err
	}
	defer os.RemoveAll(stage)
	if err := extractZipEntries(ctx, stage, entries); err != nil {
		return RestoreResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RestoreResult{}, err
	}

	result := RestoreResult{Backup: name}
	snapshots := filepath.Join(b.paths.Backups, "before-restore")
	if _, err := os.Lstat(b.paths.UserData); err == nil {
		if err := os.MkdirAll(snapshots, 0700); err != nil {
			return RestoreResult{}, err
		}
		previous := filepath.Join(snapshots, "userdata-"+stamp)
		for n := 2; exists(previous); n++ {
			previous = filepath.Join(snapshots, fmt.Sprintf("userdata-%s-%d", stamp, n))
		}
		if err := os.Rename(b.paths.UserData, previous); err != nil {
			return RestoreResult{}, fmt.Errorf("move the current userdata aside (is a file in it open?): %w", err)
		}
		result.Previous = previous
	} else if !errors.Is(err, os.ErrNotExist) {
		return RestoreResult{}, err
	}
	if err := os.Rename(stage, b.paths.UserData); err != nil {
		if result.Previous != "" {
			if rollbackErr := os.Rename(result.Previous, b.paths.UserData); rollbackErr != nil {
				return RestoreResult{}, fmt.Errorf("restore failed: %w; the previous userdata is kept at %s", err, result.Previous)
			}
		}
		return RestoreResult{}, err
	}
	pruneRestoreSnapshots(snapshots, keptRestoreSnapshots)
	return result, nil
}

// pruneRestoreSnapshots keeps the newest snapshots; names sort by time.
func pruneRestoreSnapshots(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "userdata-") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.RemoveAll(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

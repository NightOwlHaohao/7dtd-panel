//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

type savePackageDir struct{ path string }

func openSavePackageDir(paths Paths) (*savePackageDir, error) {
	for _, path := range []string{paths.Backups, paths.SavePackages} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if err = os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
				return nil, err
			}
			info, err = os.Lstat(path)
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeType != os.ModeDir {
			return nil, ErrUnsafeSavePath
		}
	}
	return &savePackageDir{path: paths.SavePackages}, nil
}

func (d *savePackageDir) Path(name string) string { return filepath.Join(d.path, name) }
func (d *savePackageDir) Create(name string) (*os.File, error) {
	return os.OpenFile(d.Path(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}
func (d *savePackageDir) Open(name string) (*os.File, error) { return os.Open(d.Path(name)) }
func (d *savePackageDir) CreateTemp(pattern string) (*os.File, string, error) {
	f, err := os.CreateTemp(d.path, pattern)
	if err != nil {
		return nil, "", err
	}
	return f, filepath.Base(f.Name()), err
}
func (d *savePackageDir) Remove(name string) error     { return os.Remove(d.Path(name)) }
func (d *savePackageDir) Rename(from, to string) error { return os.Rename(d.Path(from), d.Path(to)) }
func (d *savePackageDir) Close() error                 { return nil }

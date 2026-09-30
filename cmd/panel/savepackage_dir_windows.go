//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	ntObjCaseInsensitive = 0x40
	ntObjDontReparse     = 0x1000

	ntSynchronize          = 0x100000
	ntDelete               = 0x10000
	ntFileListDirectory    = 0x1
	ntFileReadData         = 0x1
	ntFileAddFile          = 0x2
	ntFileAddSubdirectory  = 0x4
	ntFileTraverse         = 0x20
	ntFileReadAttributes   = 0x80
	ntFileShareAll         = 0x7
	ntFileAttributeNormal  = 0x80
	ntFileOpen             = 1
	ntFileCreate           = 2
	ntFileOpenIf           = 3
	ntFileDirectory        = 0x1
	ntFileNonDirectory     = 0x40
	ntFileSynchronous      = 0x20
	ntFileBackupIntent     = 0x4000
	ntFileOpenReparsePoint = 0x200000
	ntFileRenameInfo       = 10
	ntFileDispositionInfo  = 13
	ntStatusReparse        = 0xC000050B
)

var (
	ntdll                     = syscall.NewLazyDLL("ntdll.dll")
	ntCreateFileProc          = ntdll.NewProc("NtCreateFile")
	ntSetInformationFileProc  = ntdll.NewProc("NtSetInformationFile")
	rtlNtStatusToDosErrorProc = ntdll.NewProc("RtlNtStatusToDosErrorNoTeb")

	// Test-only seam: swaps an ancestor after it is opened but before its child is created.
	beforeSavePackageChild func(string)
	// Test-only seam: removes a staged upload before reopening it.
	beforeSavePackageOpen func(*savePackageDir, string)
)

type ntUnicodeString struct {
	Length, MaximumLength uint16
	Buffer                *uint16
}

type ntObjectAttributes struct {
	Length             uint32
	RootDirectory      syscall.Handle
	ObjectName         *ntUnicodeString
	Attributes         uint32
	SecurityDescriptor uintptr
	SecurityQoS        uintptr
}

type ntIOStatusBlock struct {
	Status      uint32
	Information uintptr
}

type ntRenameInformation struct {
	ReplaceIfExists byte
	_               [7]byte
	RootDirectory   syscall.Handle
	FileNameLength  uint32
	FileName        [32767]uint16
}

type ntDispositionInformation struct{ DeleteFile byte }

type savePackageDir struct {
	path    string
	handles []syscall.Handle
}

func openSavePackageDir(paths Paths) (*savePackageDir, error) {
	root, err := openSavePackageRoot(paths.Root)
	if err != nil {
		return nil, fmt.Errorf("open package root: %w", err)
	}
	backups, err := ntOpenDirectory(root, "backups", true)
	if err != nil {
		syscall.CloseHandle(root)
		return nil, fmt.Errorf("open backups directory: %w", err)
	}
	if beforeSavePackageChild != nil {
		beforeSavePackageChild("saves")
	}
	saves, err := ntOpenDirectory(backups, "saves", true)
	if err != nil {
		syscall.CloseHandle(backups)
		syscall.CloseHandle(root)
		return nil, fmt.Errorf("open save package directory: %w", err)
	}
	return &savePackageDir{path: paths.SavePackages, handles: []syscall.Handle{root, backups, saves}}, nil
}

func openSavePackageRoot(path string) (syscall.Handle, error) {
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	h, err := syscall.CreateFile(path16, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		syscall.CloseHandle(h)
		return syscall.InvalidHandle, err
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		syscall.CloseHandle(h)
		return syscall.InvalidHandle, fmt.Errorf("%w: %s", ErrUnsafeSavePath, path)
	}
	return h, nil
}

func ntOpenDirectory(root syscall.Handle, name string, create bool) (syscall.Handle, error) {
	disposition := uint32(ntFileOpen)
	if create {
		disposition = ntFileOpenIf
	}
	return ntCreateFile(root, name,
		ntSynchronize|ntFileListDirectory|ntFileTraverse|ntFileAddFile|ntFileAddSubdirectory|ntFileReadAttributes,
		disposition, ntFileDirectory|ntFileSynchronous|ntFileBackupIntent)
}

func (d *savePackageDir) Path(name string) string { return filepath.Join(d.path, name) }

func (d *savePackageDir) Create(name string) (*os.File, error) {
	h, err := ntCreateFile(d.handles[len(d.handles)-1], name, 0x12019e, ntFileCreate, ntFileNonDirectory|ntFileSynchronous|ntFileOpenReparsePoint)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), d.Path(name)), nil
}

func (d *savePackageDir) Open(name string) (*os.File, error) {
	if beforeSavePackageOpen != nil {
		beforeSavePackageOpen(d, name)
	}
	h, err := ntCreateFile(d.handles[len(d.handles)-1], name, ntSynchronize|ntFileReadData|ntFileReadAttributes, ntFileOpen, ntFileNonDirectory|ntFileSynchronous|ntFileOpenReparsePoint)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), d.Path(name)), nil
}

func (d *savePackageDir) CreateTemp(pattern string) (*os.File, string, error) {
	star := strings.IndexByte(pattern, '*')
	if star < 0 {
		return nil, "", fmt.Errorf("temporary pattern %q has no wildcard", pattern)
	}
	for range 100 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := pattern[:star] + hex.EncodeToString(random[:]) + pattern[star+1:]
		f, err := d.Create(name)
		if err == nil {
			return f, name, nil
		}
		if errno, ok := err.(syscall.Errno); !ok || errno != syscall.ERROR_FILE_EXISTS {
			return nil, "", err
		}
	}
	return nil, "", os.ErrExist
}

func (d *savePackageDir) Rename(from, to string) error {
	h, err := ntCreateFile(d.handles[len(d.handles)-1], from, ntSynchronize|ntDelete|ntFileReadAttributes, ntFileOpen, ntFileNonDirectory|ntFileSynchronous|ntFileOpenReparsePoint)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	name, err := syscall.UTF16FromString(to)
	if err != nil {
		return err
	}
	info := &ntRenameInformation{ReplaceIfExists: 1, RootDirectory: d.handles[len(d.handles)-1], FileNameLength: uint32((len(name) - 1) * 2)}
	copy(info.FileName[:], name[:len(name)-1])
	return ntSetInformationFile(h, unsafe.Pointer(info), uint32(unsafe.Sizeof(*info)), ntFileRenameInfo)
}

func (d *savePackageDir) Remove(name string) error {
	h, err := ntCreateFile(d.handles[len(d.handles)-1], name, ntSynchronize|ntDelete|ntFileReadAttributes, ntFileOpen, ntFileNonDirectory|ntFileSynchronous|ntFileOpenReparsePoint)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	return ntSetInformationFile(h, unsafe.Pointer(&ntDispositionInformation{DeleteFile: 1}), uint32(unsafe.Sizeof(ntDispositionInformation{})), ntFileDispositionInfo)
}

func (d *savePackageDir) Close() error {
	var err error
	for i := len(d.handles) - 1; i >= 0; i-- {
		if closeErr := syscall.CloseHandle(d.handles[i]); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}

func ntCreateFile(root syscall.Handle, name string, access, disposition, options uint32) (syscall.Handle, error) {
	name16, err := syscall.UTF16FromString(name)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	objectName := &ntUnicodeString{Length: uint16((len(name16) - 1) * 2), MaximumLength: uint16(len(name16) * 2), Buffer: &name16[0]}
	attrs := ntObjectAttributes{Length: uint32(unsafe.Sizeof(ntObjectAttributes{})), RootDirectory: root, ObjectName: objectName, Attributes: ntObjCaseInsensitive | ntObjDontReparse}
	var handle syscall.Handle
	var status ntIOStatusBlock
	r, _, _ := ntCreateFileProc.Call(uintptr(unsafe.Pointer(&handle)), uintptr(access), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&status)), 0, ntFileAttributeNormal, ntFileShareAll, uintptr(disposition), uintptr(options), 0, 0)
	if uint32(r) != 0 {
		if uint32(r) == ntStatusReparse {
			return syscall.InvalidHandle, fmt.Errorf("%w: %s", ErrUnsafeSavePath, name)
		}
		return syscall.InvalidHandle, ntStatusError(uint32(r))
	}
	return handle, nil
}

func ntSetInformationFile(handle syscall.Handle, data unsafe.Pointer, size, class uint32) error {
	var status ntIOStatusBlock
	r, _, _ := ntSetInformationFileProc.Call(uintptr(handle), uintptr(unsafe.Pointer(&status)), uintptr(data), uintptr(size), uintptr(class))
	if uint32(r) != 0 {
		return ntStatusError(uint32(r))
	}
	return nil
}

func ntStatusError(status uint32) error {
	r, _, _ := rtlNtStatusToDosErrorProc.Call(uintptr(status))
	return syscall.Errno(r)
}

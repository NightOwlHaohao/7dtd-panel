package main

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

type WorldKind string

const (
	WorldOfficial  WorldKind = "official"
	WorldPregen    WorldKind = "pregen"
	WorldBuiltin   WorldKind = "builtin"
	WorldGenerated WorldKind = "generated"
	WorldMissing   WorldKind = "missing"
)

type SaveInfo struct {
	World    string    `json:"world"`
	Name     string    `json:"name"`
	Active   bool      `json:"active"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type WorldInfo struct {
	Name    string     `json:"name"`
	Kind    WorldKind  `json:"kind"`
	Missing bool       `json:"missing"`
	Saves   []SaveInfo `json:"saves"`
}

type SaveCatalog struct {
	Worlds []WorldInfo `json:"worlds"`
}

type SaveService struct {
	paths                         Paths
	server                        *ServerManager
	rename                        func(string, string) error
	beforeSavePackageVerification func(string) // Test-only seam for swaps after export and before verification.
}

var ErrUnsafeSavePath = errors.New("unsafe world or save path")

type SavePackageManifest struct {
	Version int       `json:"version"`
	Kind    WorldKind `json:"kind"`
	World   string    `json:"world"`
	Game    string    `json:"game"`
}

func (s SaveService) Export(ctx context.Context, world, game string) (BackupInfo, error) {
	release, err := s.acquireStoppedOperation()
	if err != nil {
		return BackupInfo{}, err
	}
	defer release()
	return s.exportLocked(ctx, world, game)
}

func (s SaveService) acquireStoppedOperation() (func(), error) {
	if s.server == nil {
		return nil, ErrServerMustBeStopped
	}
	release, err := s.server.BeginStoppedOperation()
	if errors.Is(err, ErrServerRunning) {
		return nil, ErrServerMustBeStopped
	}
	return release, err
}

func (s SaveService) exportLocked(ctx context.Context, world, game string) (BackupInfo, error) {
	if err := safeSaveComponent(world); err != nil {
		return BackupInfo{}, err
	}
	if err := safeSaveComponent(game); err != nil {
		return BackupInfo{}, err
	}

	saveRoot := filepath.Join(s.paths.Saves, world, game)
	if err := strictSaveDirChain(s.paths.Saves, world, game); err != nil {
		return BackupInfo{}, err
	}
	generatedRoot := filepath.Join(s.paths.GeneratedWorlds, world)
	generated, err := strictSaveDirChainIfExists(s.paths.GeneratedWorlds, world)
	if err != nil {
		return BackupInfo{}, err
	}
	builtin, err := strictSaveDirChainIfExists(s.paths.BuiltinWorlds, world)
	if err != nil {
		return BackupInfo{}, err
	}
	kind := WorldMissing
	if builtin {
		kind = WorldBuiltin
		if world == "Navezgane" {
			kind = WorldOfficial
		} else if strings.HasPrefix(world, "Pregen") {
			kind = WorldPregen
		}
	} else if generated {
		kind = WorldGenerated
	}

	packageDir, err := openSavePackageDir(s.paths)
	if err != nil {
		return BackupInfo{}, err
	}
	defer packageDir.Close()
	created := time.Now()
	name := "save-" + created.Format("20060102-150405") + "-" + savePackageComponent(world) + "-" + savePackageComponent(game) + ".zip"
	tmpName := name + ".tmp"
	f, err := packageDir.Create(tmpName)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("create save package: %w", err)
	}
	defer func() { _ = packageDir.Remove(tmpName) }()
	stopOutput := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stopOutput()
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		return BackupInfo{}, err
	}

	w := zip.NewWriter(f)
	err = writeSavePackageManifest(w, SavePackageManifest{Version: 1, Kind: kind, World: world, Game: game})
	if err == nil {
		err = writeSavePackageRoot(ctx, w, saveRoot, "save")
	}
	if err == nil && kind == WorldGenerated {
		err = writeSavePackageRoot(ctx, w, generatedRoot, "world")
	}
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	stopOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		_ = f.Close()
		return BackupInfo{}, ctxErr
	}
	if err != nil {
		_ = f.Close()
		return BackupInfo{}, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return BackupInfo{}, fmt.Errorf("sync save package: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return BackupInfo{}, fmt.Errorf("stat save package: %w", err)
	}
	if err := f.Close(); err != nil {
		return BackupInfo{}, fmt.Errorf("close save package: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return BackupInfo{}, err
	}
	if s.rename != nil {
		err = s.rename(packageDir.Path(tmpName), packageDir.Path(name))
	} else {
		err = packageDir.Rename(tmpName, name)
	}
	if err != nil {
		return BackupInfo{}, fmt.Errorf("finalize save package: %w", err)
	}
	return BackupInfo{Name: name, Created: created, Size: info.Size()}, nil
}

func (s SaveService) Switch(world, game, expectedHash string) (string, error) {
	if err := safeSaveComponent(world); err != nil {
		return "", err
	}
	if err := safeSaveComponent(game); err != nil {
		return "", err
	}
	release, err := s.acquireStoppedOperation()
	if err != nil {
		return "", err
	}
	defer release()
	catalog, err := s.Catalog("", "")
	if err != nil {
		return "", err
	}
	if !catalogHasSave(catalog, world, game) {
		return "", fmt.Errorf("save %q/%q is not in the catalog", world, game)
	}
	return SaveConfig(s.paths.ServerConfig, s.paths.ConfigBackups, expectedHash, map[string]string{"GameWorld": world, "GameName": game})
}

func catalogHasSave(catalog SaveCatalog, world, game string) bool {
	for _, candidate := range catalog.Worlds {
		if candidate.Name != world {
			continue
		}
		for _, save := range candidate.Saves {
			if save.Name == game {
				return true
			}
		}
	}
	return false
}

func (s SaveService) Delete(ctx context.Context, world, game string) (BackupInfo, error) {
	release, err := s.acquireStoppedOperation()
	if err != nil {
		return BackupInfo{}, err
	}
	defer release()
	backup, err := s.exportLocked(ctx, world, game)
	if err != nil {
		return BackupInfo{}, err
	}
	if err := s.verifyExportedSavePackage(backup.Name); err != nil {
		return BackupInfo{}, err
	}
	saveRoot := filepath.Join(s.paths.Saves, world, game)
	if err := strictSaveDirChain(s.paths.Saves, world, game); err != nil {
		return BackupInfo{}, err
	}
	if err := os.RemoveAll(saveRoot); err != nil {
		return BackupInfo{}, err
	}
	return backup, nil
}

const (
	maxImportSize      uint64 = 256 << 30
	maxModImportSize   uint64 = 16 << 30
	maxImportEntries          = 100000
	importDiskHeadroom uint64 = 1 << 30
)

type importPackage struct {
	manifest     SavePackageManifest
	entries      []zipEntry
	expandedSize uint64
}

func (s SaveService) Import(ctx context.Context, archivePath string) error {
	release, err := s.acquireStoppedOperation()
	if err != nil {
		return err
	}
	defer release()
	return s.importLocked(ctx, archivePath)
}

func (s SaveService) importLocked(ctx context.Context, archivePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		return err
	}
	return s.importLockedFile(ctx, archive, info.Size())
}

func (s SaveService) importLockedFile(ctx context.Context, archive io.ReaderAt, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := zip.NewReader(archive, size)
	if err != nil {
		return err
	}
	pkg, err := inspectSavePackage(reader.File)
	if err != nil {
		return err
	}
	if err := strictSaveDir(s.paths.UserData); err != nil {
		return err
	}
	if err := ensureImportSpace(s.paths.UserData, pkg.expandedSize); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(s.paths.UserData, ".import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := extractZipEntries(ctx, stage, pkg.entries); err != nil {
		return err
	}
	return s.replaceImportedPackage(ctx, stage, pkg.manifest)
}

func inspectSavePackage(files []*zip.File) (importPackage, error) {
	if len(files) > maxImportEntries {
		return importPackage{}, fmt.Errorf("%w: package has too many entries", ErrUnsafeSavePath)
	}
	var manifest *zip.File
	var total uint64
	pkg := importPackage{}
	for _, file := range files {
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return importPackage{}, fmt.Errorf("%w: symlink package entry %q", ErrUnsafeSavePath, file.Name)
		}
		size := file.UncompressedSize64
		if size > maxImportSize-total {
			return importPackage{}, fmt.Errorf("%w: package is too large", ErrUnsafeSavePath)
		}
		total += size
		if file.Name == "manifest.json" {
			if manifest != nil || !mode.IsRegular() {
				return importPackage{}, fmt.Errorf("%w: manifest.json", ErrUnsafeSavePath)
			}
			manifest = file
			continue
		}
		name, directory, err := safePackageEntry(file.Name, mode)
		if err != nil {
			return importPackage{}, err
		}
		root := strings.Split(name, "/")[0]
		if root != "save" && root != "world" {
			return importPackage{}, fmt.Errorf("%w: package entry %q", ErrUnsafeSavePath, file.Name)
		}
		pkg.entries = append(pkg.entries, zipEntry{file: file, name: name, directory: directory})
	}
	if manifest == nil {
		return importPackage{}, fmt.Errorf("%w: manifest.json", ErrUnsafeSavePath)
	}
	decoded, err := readSavePackageManifest(manifest)
	if err != nil {
		return importPackage{}, err
	}
	if err := validateSavePackageManifest(decoded); err != nil {
		return importPackage{}, err
	}
	pkg.manifest = decoded
	pkg.expandedSize = total
	for _, entry := range pkg.entries {
		if strings.HasPrefix(entry.name, "world/") || entry.name == "world" {
			if decoded.Kind != WorldGenerated {
				return importPackage{}, fmt.Errorf("%w: package entry %q", ErrUnsafeSavePath, entry.file.Name)
			}
		}
	}
	return pkg, nil
}

func readSavePackageManifest(file *zip.File) (SavePackageManifest, error) {
	if file.UncompressedSize64 > 1<<20 {
		return SavePackageManifest{}, fmt.Errorf("%w: manifest is too large", ErrUnsafeSavePath)
	}
	in, err := file.Open()
	if err != nil {
		return SavePackageManifest{}, err
	}
	defer in.Close()
	b, err := io.ReadAll(io.LimitReader(in, 1<<20+1))
	if err != nil {
		return SavePackageManifest{}, err
	}
	if len(b) > 1<<20 {
		return SavePackageManifest{}, fmt.Errorf("%w: manifest is too large", ErrUnsafeSavePath)
	}
	var manifest SavePackageManifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return SavePackageManifest{}, err
	}
	return manifest, nil
}

func validateSavePackageManifest(manifest SavePackageManifest) error {
	if manifest.Version != 1 {
		return fmt.Errorf("%w: package version", ErrUnsafeSavePath)
	}
	switch manifest.Kind {
	case WorldOfficial, WorldPregen, WorldBuiltin, WorldGenerated:
	default:
		return fmt.Errorf("%w: package world kind", ErrUnsafeSavePath)
	}
	if err := safeSaveComponent(manifest.World); err != nil {
		return err
	}
	return safeSaveComponent(manifest.Game)
}

func safePackageEntry(name string, mode os.FileMode) (string, bool, error) {
	trimmed, directory, err := safeZipEntryName(name, mode, ErrUnsafeSavePath)
	if err == nil && !directory && !strings.Contains(trimmed, "/") {
		err = fmt.Errorf("%w: package entry %q", ErrUnsafeSavePath, name)
	}
	return trimmed, directory, err
}

func (s SaveService) replaceImportedPackage(ctx context.Context, stage string, manifest SavePackageManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureImportDir(s.paths.UserData); err != nil {
		return err
	}
	if err := ensureImportDir(s.paths.Saves); err != nil {
		return err
	}
	saveParent := filepath.Join(s.paths.Saves, manifest.World)
	if err := ensureImportDir(saveParent); err != nil {
		return err
	}
	targets := []importTarget{{stage: filepath.Join(stage, "save"), target: filepath.Join(saveParent, manifest.Game)}}
	if manifest.Kind == WorldGenerated {
		if err := ensureImportDir(s.paths.GeneratedWorlds); err != nil {
			return err
		}
		targets = append([]importTarget{{stage: filepath.Join(stage, "world"), target: filepath.Join(s.paths.GeneratedWorlds, manifest.World)}}, targets...)
	}
	for i := range targets {
		exists, err := strictSaveDirIfExists(targets[i].target)
		if err != nil {
			return err
		}
		targets[i].exists = exists
		if err := strictSaveDir(targets[i].stage); err != nil {
			return err
		}
	}
	if anyImportTargetExists(targets) {
		backup, err := s.exportLocked(ctx, manifest.World, manifest.Game)
		if err != nil {
			return err
		}
		if err := s.verifyExportedSavePackage(backup.Name); err != nil {
			return err
		}
	}
	for i := range targets {
		if !targets[i].exists {
			continue
		}
		previous, err := previousImportPath(targets[i].target)
		if err != nil {
			return rollbackImportTargets(s, targets, err)
		}
		targets[i].previous = previous
		if err := s.renameFile(targets[i].target, previous); err != nil {
			return rollbackImportTargets(s, targets, err)
		}
		targets[i].moved = true
	}
	for i := range targets {
		if err := s.renameFile(targets[i].stage, targets[i].target); err != nil {
			return rollbackImportTargets(s, targets, err)
		}
		targets[i].installed = true
	}
	for _, target := range targets {
		if target.moved {
			if err := os.RemoveAll(target.previous); err != nil {
				return err
			}
		}
	}
	return nil
}

type importTarget struct {
	stage, target, previous  string
	exists, moved, installed bool
}

func ensureImportDir(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeType != os.ModeDir {
		return fmt.Errorf("%w: %s", ErrUnsafeSavePath, path)
	}
	return nil
}

func anyImportTargetExists(targets []importTarget) bool {
	for _, target := range targets {
		if target.exists {
			return true
		}
	}
	return false
}

func previousImportPath(target string) (string, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(target), ".previous-"+hex.EncodeToString(token[:])), nil
}

func rollbackImportTargets(s SaveService, targets []importTarget, original error) error {
	var rollback error
	for i := len(targets) - 1; i >= 0; i-- {
		target := targets[i]
		if target.installed {
			if err := s.renameFile(target.target, target.stage); err != nil {
				rollback = errors.Join(rollback, err)
				continue
			}
		}
		if target.moved {
			if err := s.renameFile(target.previous, target.target); err != nil {
				rollback = errors.Join(rollback, err)
			}
		}
	}
	return errors.Join(original, rollback)
}

func (s SaveService) verifyExportedSavePackage(name string) error {
	if s.beforeSavePackageVerification != nil {
		s.beforeSavePackageVerification(name)
	}
	dir, err := openSavePackageDir(s.paths)
	if err != nil {
		return err
	}
	defer dir.Close()
	archive, err := dir.Open(name)
	if err != nil {
		return err
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		return err
	}
	return verifySavePackage(archive, info.Size())
}

func verifySavePackage(archive io.ReaderAt, size int64) error {
	reader, err := zip.NewReader(archive, size)
	if err != nil {
		return err
	}
	if _, err := inspectSavePackage(reader.File); err != nil {
		return err
	}
	for _, file := range reader.File {
		if !file.Mode().IsRegular() {
			continue
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(io.Discard, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (s SaveService) renameFile(from, to string) error {
	if s.rename != nil {
		return s.rename(from, to)
	}
	return os.Rename(from, to)
}

func strictSaveDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeType != os.ModeDir {
		return fmt.Errorf("%w: %s", ErrUnsafeSavePath, path)
	}
	return nil
}

func strictSaveDirIfExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeType != os.ModeDir {
		return false, fmt.Errorf("%w: %s", ErrUnsafeSavePath, path)
	}
	return true, nil
}

func strictSaveDirChain(root string, components ...string) error {
	if err := strictSaveDir(root); err != nil {
		return err
	}
	for _, component := range components {
		root = filepath.Join(root, component)
		if err := strictSaveDir(root); err != nil {
			return err
		}
	}
	return nil
}

func strictSaveDirChainIfExists(root string, components ...string) (bool, error) {
	exists, err := strictSaveDirIfExists(root)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	for _, component := range components {
		root = filepath.Join(root, component)
		exists, err = strictSaveDirIfExists(root)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
	}
	return true, nil
}

func writeSavePackageManifest(w *zip.Writer, manifest SavePackageManifest) error {
	entry, err := w.Create("manifest.json")
	if err != nil {
		return err
	}
	return json.NewEncoder(entry).Encode(manifest)
}

func writeSavePackageRoot(ctx context.Context, w *zip.Writer, root, prefix string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeType != os.ModeDir && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", ErrUnsafeSavePath, path)
		}
		name, err := zipEntryName(root, path)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnsafeSavePath, err)
		}
		name = prefix + "/" + name
		if info.IsDir() {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = name + "/"
			_, err = w.CreateHeader(header)
			return err
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
		if closeErr != nil {
			return closeErr
		}
		return ctx.Err()
	})
}

func savePackageComponent(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
}

func (s SaveService) Catalog(activeWorld, activeGame string) (SaveCatalog, error) {
	worlds := map[string]WorldInfo{}
	if err := s.addWorlds(worlds, s.paths.BuiltinWorlds, WorldBuiltin); err != nil {
		return SaveCatalog{}, err
	}
	if err := s.addWorlds(worlds, s.paths.GeneratedWorlds, WorldGenerated); err != nil {
		return SaveCatalog{}, err
	}
	entries, err := readDir(s.paths.Saves)
	if err != nil {
		return SaveCatalog{}, err
	}
	for _, entry := range entries {
		if !ordinaryDir(entry) {
			continue
		}
		if _, err := entry.Info(); err != nil {
			return SaveCatalog{}, err
		}
		name := entry.Name()
		world, ok := worlds[name]
		if !ok {
			world = WorldInfo{Name: name, Kind: WorldMissing, Missing: true}
		}
		if err := s.addSaves(&world, filepath.Join(s.paths.Saves, name), activeWorld, activeGame); err != nil {
			return SaveCatalog{}, err
		}
		worlds[name] = world
	}

	catalog := SaveCatalog{Worlds: make([]WorldInfo, 0, len(worlds))}
	for _, world := range worlds {
		sort.Slice(world.Saves, func(i, j int) bool {
			return strings.ToLower(world.Saves[i].Name) < strings.ToLower(world.Saves[j].Name)
		})
		catalog.Worlds = append(catalog.Worlds, world)
	}
	sort.Slice(catalog.Worlds, func(i, j int) bool {
		return strings.ToLower(catalog.Worlds[i].Name) < strings.ToLower(catalog.Worlds[j].Name)
	})
	return catalog, nil
}

func (s SaveService) addWorlds(worlds map[string]WorldInfo, root string, kind WorldKind) error {
	entries, err := readDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !ordinaryDir(entry) {
			continue
		}
		if _, err := entry.Info(); err != nil {
			return err
		}
		name, worldKind := entry.Name(), kind
		if _, exists := worlds[name]; exists {
			continue
		}
		if kind == WorldBuiltin {
			if name == "Navezgane" {
				worldKind = WorldOfficial
			} else if strings.HasPrefix(name, "Pregen") {
				worldKind = WorldPregen
			}
		}
		worlds[name] = WorldInfo{Name: name, Kind: worldKind}
	}
	return nil
}

func (s SaveService) addSaves(world *WorldInfo, root, activeWorld, activeGame string) error {
	entries, err := readDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !ordinaryDir(entry) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size, modified, err := saveStats(filepath.Join(root, entry.Name()), info.ModTime())
		if err != nil {
			return err
		}
		world.Saves = append(world.Saves, SaveInfo{World: world.Name, Name: entry.Name(), Active: strings.EqualFold(world.Name, activeWorld) && strings.EqualFold(entry.Name(), activeGame), Size: size, Modified: modified})
	}
	return nil
}

func readDir(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || info.Mode()&os.ModeType != os.ModeDir {
		return nil, err
	}
	return os.ReadDir(path)
}

func ordinaryDir(entry os.DirEntry) bool { return entry.Type() == os.ModeDir }

func saveStats(root string, modified time.Time) (int64, time.Time, error) {
	var size int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		typ := entry.Type()
		if typ == os.ModeDir {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.ModTime().After(modified) {
				modified = info.ModTime()
			}
			return nil
		}
		if typ != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			size += info.Size()
			if info.ModTime().After(modified) {
				modified = info.ModTime()
			}
		}
		return nil
	})
	return size, modified, err
}

func safeSaveComponent(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || filepath.VolumeName(name) != "" || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("invalid save path component %q", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid save path component %q", name)
		}
	}
	return nil
}

package main

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type DependencyPolicy string

const (
	DependencyWarn   DependencyPolicy = "warn"
	DependencyStrict DependencyPolicy = "strict"
	DependencyIgnore DependencyPolicy = "ignore"
)

type ModProblem struct {
	Kind       string `json:"kind"`
	Dependency string `json:"dependency,omitempty"`
	Constraint string `json:"constraint,omitempty"`
}

type ModInfo struct {
	ID, Name, Version, Author, Description, Path string
	Enabled, Unknown                             bool
	Dependencies                                 []ModDependency
	Problems                                     []ModProblem
}

type ModDependency struct {
	ID, Constraint string
}

type ModCatalog struct {
	Mods       []ModInfo
	Policy     DependencyPolicy
	Locked     bool
	LockReason string
}

var (
	ErrModPathEscape = errors.New("mod path escapes its root")
	ErrModsLocked    = errors.New("mods are locked while the server is active")
	ErrModRequired   = errors.New("mod is required by an enabled mod")
	ErrModConflict   = errors.New("mod destination already exists")
)

type ModService struct {
	paths  Paths
	server *ServerManager
}

func (s ModService) SetEnabled(name string, enabled bool) (ModCatalog, error) {
	release, err := s.lock()
	if err != nil {
		return ModCatalog{}, err
	}
	defer release()
	if err := safeModName(name); err != nil {
		return ModCatalog{}, err
	}
	policy, err := s.Policy()
	if err != nil {
		return ModCatalog{}, err
	}
	sourceRoot, destinationRoot := s.paths.DisabledMods, s.paths.Mods
	if !enabled {
		sourceRoot, destinationRoot = s.paths.Mods, s.paths.DisabledMods
	}
	canonicalSourceRoot, err := canonicalModRoot(sourceRoot)
	if err != nil {
		return ModCatalog{}, err
	}
	source, err := verifiedModChild(sourceRoot, canonicalSourceRoot, name)
	if err != nil {
		return ModCatalog{}, err
	}
	canonicalDestinationRoot, err := ensureModRoot(destinationRoot)
	if err != nil {
		return ModCatalog{}, err
	}
	// Build the destination from the canonical root: comparing it with a
	// non-canonical path fails whenever the panel folder is reached through
	// an 8.3 short name, junction or symlink.
	destination := filepath.Join(canonicalDestinationRoot, name)
	if !modPathWithin(canonicalDestinationRoot, destination) {
		return ModCatalog{}, fmt.Errorf("%w: %s", ErrModPathEscape, destination)
	}
	if _, err := os.Lstat(destination); err == nil {
		return ModCatalog{}, fmt.Errorf("%w: %s", ErrModConflict, destination)
	} else if !os.IsNotExist(err) {
		return ModCatalog{}, err
	}
	if !enabled {
		catalog, err := ScanMods(s.paths, DependencyWarn)
		if err != nil {
			return ModCatalog{}, err
		}
		if dependent := requiredByEnabledMod(catalog, source); dependent != "" {
			return ModCatalog{}, fmt.Errorf("%w: %s", ErrModRequired, dependent)
		}
	}
	if err := os.Rename(source, destination); err != nil {
		return ModCatalog{}, err
	}
	return ScanMods(s.paths, policy)
}

type modImportPackage struct {
	entries      []zipEntry
	roots        []string
	expandedSize uint64
}

func (s ModService) importLockedFile(ctx context.Context, archive io.ReaderAt, size int64, archiveName string) (ModCatalog, error) {
	if err := ctx.Err(); err != nil {
		return ModCatalog{}, err
	}
	reader, err := zip.NewReader(archive, size)
	if err != nil {
		return ModCatalog{}, err
	}
	pkg, err := inspectModPackage(reader.File, strings.TrimSuffix(filepath.Base(archiveName), filepath.Ext(archiveName)))
	if err != nil {
		return ModCatalog{}, err
	}
	root, err := ensureModRoot(s.paths.Mods)
	if err != nil {
		return ModCatalog{}, err
	}
	if err := ensureImportSpace(root, pkg.expandedSize); err != nil {
		return ModCatalog{}, err
	}
	for _, name := range pkg.roots {
		destination := filepath.Join(root, name)
		if !modPathWithin(root, destination) {
			return ModCatalog{}, fmt.Errorf("%w: %s", ErrModPathEscape, destination)
		}
		if _, err := os.Lstat(destination); err == nil {
			return ModCatalog{}, fmt.Errorf("%w: %s", ErrModConflict, destination)
		} else if !os.IsNotExist(err) {
			return ModCatalog{}, err
		}
	}
	stage, err := os.MkdirTemp(root, ".mod-import-")
	if err != nil {
		return ModCatalog{}, err
	}
	defer os.RemoveAll(stage)
	if err := extractZipEntries(ctx, stage, pkg.entries); err != nil {
		return ModCatalog{}, err
	}
	for _, name := range pkg.roots {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(root, name)); err != nil {
			return ModCatalog{}, err
		}
	}
	// Remove the now empty stage before scanning, or it is listed as a mod.
	if err := os.RemoveAll(stage); err != nil {
		return ModCatalog{}, err
	}
	policy, err := s.Policy()
	if err != nil {
		return ModCatalog{}, err
	}
	return ScanMods(s.paths, policy)
}

func inspectModPackage(files []*zip.File, fallbackName string) (modImportPackage, error) {
	if len(files) > maxImportEntries {
		return modImportPackage{}, fmt.Errorf("%w: package has too many entries", ErrModPathEscape)
	}
	var entries []zipEntry
	var total uint64
	seen := map[string]bool{}
	for _, file := range files {
		if file.Mode()&os.ModeSymlink != 0 || file.UncompressedSize64 > maxModImportSize-total {
			return modImportPackage{}, fmt.Errorf("%w: package entry %q", ErrModPathEscape, file.Name)
		}
		total += file.UncompressedSize64
		name, directory, err := safeZipEntryName(file.Name, file.Mode(), ErrModPathEscape)
		if err != nil {
			return modImportPackage{}, err
		}
		if seen[name] {
			return modImportPackage{}, fmt.Errorf("%w: duplicate package entry %q", ErrModPathEscape, file.Name)
		}
		seen[name] = true
		entries = append(entries, zipEntry{file: file, name: name, directory: directory})
	}
	entries, err := normalizeModLayout(entries, fallbackName)
	if err != nil {
		return modImportPackage{}, err
	}
	pkg := modImportPackage{entries: entries, expandedSize: total}
	roots, metadata := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		parts := strings.Split(entry.name, "/")
		if safeModName(parts[0]) != nil || len(parts) == 1 && !entry.directory {
			return modImportPackage{}, fmt.Errorf("%w: package entry %q", ErrModPathEscape, entry.file.Name)
		}
		roots[parts[0]] = true
		if len(parts) == 2 && parts[1] == "ModInfo.xml" && !entry.directory {
			metadata[parts[0]] = true
		}
	}
	for root := range roots {
		if !metadata[root] {
			return modImportPackage{}, fmt.Errorf("%w: %s/ModInfo.xml", ErrModPathEscape, root)
		}
		pkg.roots = append(pkg.roots, root)
	}
	if len(pkg.roots) == 0 {
		return modImportPackage{}, fmt.Errorf("%w: package is empty", ErrModPathEscape)
	}
	sort.Strings(pkg.roots)
	return pkg, nil
}

// normalizeModLayout accepts the common ways mods are zipped and maps them
// onto the canonical <ModName>/ModInfo.xml layout:
//   - ModName/ModInfo.xml            (canonical, unchanged)
//   - Wrapper/ModName/ModInfo.xml    (one extra folder such as "Mods/";
//     loose files directly in the wrapper, e.g. a readme, are skipped)
//   - ModInfo.xml at the archive root (the mod is named after the ZIP)
func normalizeModLayout(entries []zipEntry, fallbackName string) ([]zipEntry, error) {
	hasFile := func(name string) bool {
		for _, entry := range entries {
			if !entry.directory && entry.name == name {
				return true
			}
		}
		return false
	}
	if hasFile("ModInfo.xml") {
		if safeModName(fallbackName) != nil {
			return nil, fmt.Errorf("%w: ModInfo.xml is at the archive root and the ZIP name %q cannot name the mod", ErrModPathEscape, fallbackName)
		}
		out := make([]zipEntry, 0, len(entries))
		for _, entry := range entries {
			entry.name = fallbackName + "/" + entry.name
			out = append(out, entry)
		}
		return out, nil
	}
	top := map[string]bool{}
	for _, entry := range entries {
		top[strings.SplitN(entry.name, "/", 2)[0]] = true
	}
	if len(top) != 1 {
		return entries, nil
	}
	var wrapper string
	for name := range top {
		wrapper = name
	}
	if hasFile(wrapper + "/ModInfo.xml") {
		return entries, nil // the single top folder is the mod itself
	}
	out := make([]zipEntry, 0, len(entries))
	for _, entry := range entries {
		rest, ok := strings.CutPrefix(entry.name, wrapper+"/")
		if !ok || (!entry.directory && !strings.Contains(rest, "/")) {
			continue // the wrapper folder itself, or a loose file inside it
		}
		entry.name = rest
		out = append(out, entry)
	}
	return out, nil
}

func (s ModService) SetPolicy(policy DependencyPolicy) error {
	if !validDependencyPolicy(policy) {
		return fmt.Errorf("invalid mod dependency policy %q", policy)
	}
	release, err := s.lock()
	if err != nil {
		return err
	}
	defer release()
	_, err = updatePanelConfig(s.paths.ModSettings, func(config *PanelConfig) { config.ModDependencyPolicy = policy })
	return err
}

func (s ModService) StartBlocker() error {
	policy, err := s.Policy()
	if err != nil || policy != DependencyStrict {
		return err
	}
	catalog, err := ScanMods(s.paths, policy)
	if err != nil {
		return err
	}
	for _, mod := range catalog.Mods {
		for _, problem := range mod.Problems {
			if problem.Kind == "missing_dependency" || problem.Kind == "version_mismatch" {
				return fmt.Errorf("mod %q: %s %q", mod.Name, problem.Kind, problem.Dependency)
			}
		}
	}
	return nil
}

func (s ModService) Policy() (DependencyPolicy, error) {
	config, err := LoadPanelConfig(s.paths.ModSettings)
	return config.ModDependencyPolicy, err
}

func (s ModService) lock() (func(), error) {
	if s.server == nil {
		return func() {}, nil
	}
	if state := s.server.Status().State; state == ServerStarting || state == ServerRunning || state == ServerStopping {
		return nil, fmt.Errorf("%w: server is %s", ErrModsLocked, state)
	}
	release, err := s.server.BeginStoppedOperation()
	if err == nil {
		return release, nil
	}
	if state := s.server.Status().State; state == ServerStarting || state == ServerRunning || state == ServerStopping {
		return nil, fmt.Errorf("%w: server is %s", ErrModsLocked, state)
	}
	return nil, err
}

func validDependencyPolicy(policy DependencyPolicy) bool {
	return policy == DependencyWarn || policy == DependencyStrict || policy == DependencyIgnore
}

func safeModName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || filepath.VolumeName(name) != "" || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: %q", ErrModPathEscape, name)
	}
	return nil
}

func verifiedModChild(root, canonicalRoot, name string) (string, error) {
	path := filepath.Join(root, name)
	canonicalPath, isDir, err := canonicalModChild(path)
	if err != nil {
		return "", err
	}
	if !isDir || !modPathWithin(canonicalRoot, canonicalPath) {
		return "", fmt.Errorf("%w: %s", ErrModPathEscape, path)
	}
	return path, nil
}

func ensureModRoot(path string) (string, error) {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	return canonicalModRoot(path)
}

func requiredByEnabledMod(catalog ModCatalog, source string) string {
	var target *ModInfo
	for i := range catalog.Mods {
		if filepath.Clean(catalog.Mods[i].Path) == filepath.Clean(source) {
			target = &catalog.Mods[i]
			break
		}
	}
	if target == nil {
		return ""
	}
	for _, mod := range catalog.Mods {
		if !mod.Enabled || filepath.Clean(mod.Path) == filepath.Clean(source) {
			continue
		}
		for _, dependency := range mod.Dependencies {
			if strings.EqualFold(dependency.ID, target.ID) {
				return mod.Name
			}
		}
	}
	return ""
}

func ScanMods(paths Paths, policy DependencyPolicy) (ModCatalog, error) {
	catalog := ModCatalog{Mods: []ModInfo{}, Policy: policy}
	for _, root := range []struct {
		path    string
		enabled bool
	}{{paths.Mods, true}, {paths.DisabledMods, false}} {
		mods, err := scanModRoot(root.path, root.enabled)
		if err != nil {
			return ModCatalog{}, err
		}
		catalog.Mods = append(catalog.Mods, mods...)
	}
	if policy != DependencyIgnore {
		addModDependencyProblems(catalog.Mods)
	}
	sort.Slice(catalog.Mods, func(i, j int) bool {
		left, right := strings.ToLower(catalog.Mods[i].Name), strings.ToLower(catalog.Mods[j].Name)
		if left == right {
			return strings.ToLower(catalog.Mods[i].Path) < strings.ToLower(catalog.Mods[j].Path)
		}
		return left < right
	})
	return catalog, nil
}

func scanModRoot(root string, enabled bool) ([]ModInfo, error) {
	canonicalRoot, err := canonicalModRoot(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	mods := make([]ModInfo, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue // import stages and other hidden folders are not mods
		}
		path := filepath.Join(root, entry.Name())
		canonicalPath, isDir, err := canonicalModChild(path)
		if err != nil {
			return nil, err
		}
		if !isDir {
			continue
		}
		if !modPathWithin(canonicalRoot, canonicalPath) {
			return nil, fmt.Errorf("%w: %s", ErrModPathEscape, path)
		}
		mod := ModInfo{ID: entry.Name(), Name: entry.Name(), Path: path, Enabled: enabled, Dependencies: []ModDependency{}, Problems: []ModProblem{}}
		parsed, err := parseModInfo(filepath.Join(path, "ModInfo.xml"))
		if err != nil {
			mod.Unknown = true
			mod.Problems = append(mod.Problems, ModProblem{Kind: "unreadable_metadata"})
		} else {
			parsed.Path, parsed.Enabled = path, enabled
			if parsed.ID == "" {
				parsed.ID = entry.Name()
			}
			if parsed.Name == "" {
				parsed.Name = entry.Name()
			}
			mod = parsed
		}
		mods = append(mods, mod)
	}
	return mods, nil
}

func canonicalModRoot(path string) (string, error) {
	if err := rejectModReparse(path); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeType != os.ModeDir {
		return "", fmt.Errorf("%w: %s", ErrModPathEscape, path)
	}
	return resolvedModPath(path)
}

func canonicalModChild(path string) (string, bool, error) {
	if err := rejectModReparse(path); err != nil {
		return "", false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeType != os.ModeDir {
		if target, statErr := os.Stat(path); statErr == nil && target.IsDir() {
			return "", false, fmt.Errorf("%w: %s", ErrModPathEscape, path)
		}
		return "", false, nil
	}
	resolved, err := resolvedModPath(path)
	return resolved, err == nil, err
}

func resolvedModPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if os.IsPermission(err) {
		if reparseErr := rejectModReparse(path); reparseErr != nil {
			return "", reparseErr
		}
		return path, nil
	}
	return "", err
}

func rejectModReparse(path string) error {
	reparse, err := modPathIsReparse(path)
	if err != nil {
		return err
	}
	if reparse {
		return fmt.Errorf("%w: %s", ErrModPathEscape, path)
	}
	return nil
}

func modPathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func parseModInfo(path string) (ModInfo, error) {
	in, err := os.Open(path)
	if err != nil {
		return ModInfo{}, err
	}
	defer in.Close()
	mod := ModInfo{Dependencies: []ModDependency{}, Problems: []ModProblem{}}
	var internalName, displayName string
	decoder := xml.NewDecoder(io.LimitReader(in, 1<<20))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ModInfo{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		name := strings.ToLower(start.Name.Local)
		if name == "dependency" {
			id := xmlAttribute(start.Attr, "id")
			if id == "" {
				id = xmlAttribute(start.Attr, "name")
			}
			constraint := xmlAttribute(start.Attr, "constraint")
			if constraint == "" {
				constraint = xmlAttribute(start.Attr, "version")
			}
			if id == "" {
				return ModInfo{}, errors.New("dependency without id")
			}
			mod.Dependencies = append(mod.Dependencies, ModDependency{ID: id, Constraint: constraint})
			continue
		}
		var target *string
		switch name {
		case "id":
			target = &mod.ID
		case "name":
			target = &internalName
		case "displayname":
			target = &displayName
		case "version":
			target = &mod.Version
		case "author":
			target = &mod.Author
		case "description":
			target = &mod.Description
		}
		if target == nil {
			continue
		}
		*target = xmlAttribute(start.Attr, "value")
		if *target == "" {
			var text string
			if err := decoder.DecodeElement(&text, &start); err != nil {
				return ModInfo{}, err
			}
			*target = strings.TrimSpace(text)
		}
	}
	if mod.ID == "" {
		mod.ID = internalName
	}
	if displayName != "" {
		mod.Name = displayName
	} else {
		mod.Name = internalName
	}
	if mod.ID == "" || mod.Name == "" || mod.Version == "" || mod.Author == "" || mod.Description == "" {
		mod.Unknown = true
		mod.Problems = append(mod.Problems, ModProblem{Kind: "unreadable_metadata"})
	}
	sort.Slice(mod.Dependencies, func(i, j int) bool {
		return strings.ToLower(mod.Dependencies[i].ID) < strings.ToLower(mod.Dependencies[j].ID)
	})
	return mod, nil
}

func xmlAttribute(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if strings.EqualFold(attr.Name.Local, name) {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

func addModDependencyProblems(mods []ModInfo) {
	enabled := make(map[string]ModInfo)
	for _, mod := range mods {
		if mod.Enabled && mod.ID != "" {
			enabled[strings.ToLower(mod.ID)] = mod
		}
	}
	for i := range mods {
		if !mods[i].Enabled {
			continue
		}
		for _, dependency := range mods[i].Dependencies {
			target, ok := enabled[strings.ToLower(dependency.ID)]
			if !ok {
				mods[i].Problems = append(mods[i].Problems, ModProblem{Kind: "missing_dependency", Dependency: dependency.ID, Constraint: dependency.Constraint})
				continue
			}
			if dependency.Constraint == "" || target.Version == dependency.Constraint {
				continue
			}
			if exactVersionConstraint(dependency.Constraint) {
				mods[i].Problems = append(mods[i].Problems, ModProblem{Kind: "version_mismatch", Dependency: dependency.ID, Constraint: dependency.Constraint})
			} else {
				mods[i].Problems = append(mods[i].Problems, ModProblem{Kind: "unsupported_constraint", Dependency: dependency.ID, Constraint: dependency.Constraint})
			}
		}
	}
}

func exactVersionConstraint(constraint string) bool {
	constraint = strings.TrimSpace(constraint)
	return constraint != "" && !strings.ContainsAny(constraint, "<>=~^*|, ")
}

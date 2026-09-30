package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModImportRejectsExcessiveExpansionAndEntryCount(t *testing.T) {
	large := &zip.File{FileHeader: zip.FileHeader{Name: "Large/payload.bin", UncompressedSize64: maxModImportSize + 1}}
	large.SetMode(0600)
	if _, err := inspectModPackage([]*zip.File{large}, "Large"); err == nil {
		t.Fatal("oversized Mod expansion was accepted")
	}
	entries := make([]*zip.File, maxImportEntries+1)
	if _, err := inspectModPackage(entries, "Many"); err == nil {
		t.Fatal("excessive Mod entry count was accepted")
	}
}

func TestScanModsReadsEnabledDisabledAndUnknownDirectories(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Core"), `<xml><Name value="core"/><DisplayName value="Core Mod"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></xml>`)
	writeModInfo(t, filepath.Join(p.DisabledMods, "Optional"), `<xml><ModInfo><ID value="optional"/><Name value="Optional Mod"/><Version value="2.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	if err := os.MkdirAll(filepath.Join(p.Mods, "Unknown"), 0700); err != nil {
		t.Fatal(err)
	}
	catalog, err := ScanMods(p, DependencyWarn)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Mods) != 3 {
		t.Fatalf("mods=%#v", catalog.Mods)
	}
	if core := findMod(t, catalog, "core"); !core.Enabled || core.Unknown || core.Name != "Core Mod" || core.Path != filepath.Join(p.Mods, "Core") {
		t.Fatalf("core=%#v", core)
	}
	if optional := findMod(t, catalog, "optional"); optional.Enabled || optional.Unknown {
		t.Fatalf("optional=%#v", optional)
	}
	if unknown := findMod(t, catalog, "Unknown"); !unknown.Unknown || !unknown.Enabled || !hasModProblem(unknown, "unreadable_metadata") {
		t.Fatalf("unknown=%#v", unknown)
	}
}

func TestScanModsKeepsMalformedMetadataVisible(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Broken"), `<xml><ModInfo>`)

	catalog, err := ScanMods(p, DependencyWarn)
	if err != nil {
		t.Fatal(err)
	}
	broken := findMod(t, catalog, "Broken")
	if !broken.Unknown || !hasModProblem(broken, "unreadable_metadata") {
		t.Fatalf("broken=%#v", broken)
	}
}

func TestPartialModMetadataKeepsDeclaredDependencies(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Core"), `<xml><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="A"/><Description value="D"/></xml>`)
	writeModInfo(t, filepath.Join(p.Mods, "Addon"), `<xml><ID value="addon"/><Name value="Addon"/><Dependencies><Dependency id="core"/></Dependencies></xml>`)
	catalog, err := ScanMods(p, DependencyStrict)
	if err != nil {
		t.Fatal(err)
	}
	addon := findMod(t, catalog, "addon")
	if !addon.Unknown || len(addon.Dependencies) != 1 || addon.Dependencies[0].ID != "core" {
		t.Fatalf("addon=%#v", addon)
	}
}

func TestModPathRejectsEscapingReparseChild(t *testing.T) {
	p, outside := ResolvePaths(t.TempDir()), t.TempDir()
	if err := os.MkdirAll(p.Mods, 0700); err != nil {
		t.Fatal(err)
	}
	writeModInfo(t, outside, `<xml><ModInfo><ID value="outside"/><Name value="Outside"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	makeReparseLink(t, outside, filepath.Join(p.Mods, "Escape"))

	if _, err := ScanMods(p, DependencyWarn); !errors.Is(err, ErrModPathEscape) {
		t.Fatalf("err=%v", err)
	}
}

func TestModPathRejectsReparseRootsBeforeReading(t *testing.T) {
	for _, test := range []struct {
		name string
		root func(Paths) string
	}{
		{"enabled", func(p Paths) string { return p.Mods }},
		{"disabled", func(p Paths) string { return p.DisabledMods }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, outside := ResolvePaths(t.TempDir()), t.TempDir()
			root := test.root(p)
			if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
				t.Fatal(err)
			}
			makeReparseLink(t, outside, root)
			if err := os.RemoveAll(outside); err != nil {
				t.Fatal(err)
			}

			if _, err := ScanMods(p, DependencyWarn); !errors.Is(err, ErrModPathEscape) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestModDependenciesReportMissingAndExactVersionMismatch(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	writeModInfo(t, filepath.Join(p.Mods, "Addon"), `<xml><ModInfo><ID value="addon"/><Name value="Addon"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/><Dependencies><Dependency id="core" version="2.0"/><Dependency id="missing" version="1.0"/></Dependencies></ModInfo></xml>`)

	catalog, err := ScanMods(p, DependencyStrict)
	if err != nil {
		t.Fatal(err)
	}
	addon := findMod(t, catalog, "addon")
	if !hasModProblem(addon, "missing_dependency") || !hasModProblem(addon, "version_mismatch") {
		t.Fatalf("addon=%#v", addon)
	}
}

func TestModDependenciesWarnUnsupportedConstraintsWithoutStrictProblem(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	writeModInfo(t, filepath.Join(p.Mods, "Addon"), `<xml><ModInfo><ID value="addon"/><Name value="Addon"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/><Dependencies><Dependency id="core" version=">=2.0"/></Dependencies></ModInfo></xml>`)

	catalog, err := ScanMods(p, DependencyStrict)
	if err != nil {
		t.Fatal(err)
	}
	if hasModProblem(findMod(t, catalog, "addon"), "version_mismatch") {
		t.Fatalf("unsupported constraint became strict mismatch: %#v", catalog)
	}
}

func TestModServiceLocksMutationsAndPolicyWhileActive(t *testing.T) {
	for _, state := range []ServerState{ServerStarting, ServerRunning, ServerStopping} {
		t.Run(string(state), func(t *testing.T) {
			p := ResolvePaths(t.TempDir())
			server := newServerManager(p, nil, &fakeProcessProbe{}, nil)
			server.mu.Lock()
			server.state = state
			server.mu.Unlock()
			service := ModService{paths: p, server: server}
			if _, err := service.SetEnabled("Core", false); !errors.Is(err, ErrModsLocked) {
				t.Fatalf("SetEnabled err=%v", err)
			}
			if err := service.SetPolicy(DependencyStrict); !errors.Is(err, ErrModsLocked) {
				t.Fatalf("SetPolicy err=%v", err)
			}
		})
	}
}

func TestModServiceRefusesToOverwriteExistingDestination(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="Author"/><Description value="source"/></ModInfo></xml>`)
	writeModInfo(t, filepath.Join(p.DisabledMods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="Author"/><Description value="destination"/></ModInfo></xml>`)
	service := ModService{paths: p}
	if _, err := service.SetEnabled("Core", false); !errors.Is(err, ErrModConflict) {
		t.Fatalf("err=%v", err)
	}
	if got := string(mustRead(t, filepath.Join(p.DisabledMods, "Core", "ModInfo.xml"))); !strings.Contains(got, "destination") {
		t.Fatalf("destination overwritten: %q", got)
	}
}

func TestModServiceMovesDirectoryAndReturnsRescan(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	writeFile(t, filepath.Join(p.Mods, "Core", "payload.txt"), "unchanged")
	catalog, err := (ModService{paths: p}).SetEnabled("Core", false)
	if err != nil {
		t.Fatal(err)
	}
	if moved := findMod(t, catalog, "core"); moved.Enabled || moved.Path != filepath.Join(p.DisabledMods, "Core") {
		t.Fatalf("moved=%#v", moved)
	}
	if got := string(mustRead(t, filepath.Join(p.DisabledMods, "Core", "payload.txt"))); got != "unchanged" {
		t.Fatalf("payload=%q", got)
	}
	if _, err := os.Lstat(filepath.Join(p.Mods, "Core")); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
}

func TestModServiceBlocksRequiredEnabledMod(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	writeModInfo(t, filepath.Join(p.Mods, "Addon"), `<xml><ModInfo><ID value="addon"/><Name value="Addon"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/><Dependencies><Dependency id="core"/></Dependencies></ModInfo></xml>`)
	service := ModService{paths: p}
	if _, err := service.SetEnabled("Core", false); !errors.Is(err, ErrModRequired) || !strings.Contains(err.Error(), "Addon") {
		t.Fatalf("err=%v", err)
	}
}

func TestModServiceRejectsReparseMoveSource(t *testing.T) {
	p, outside := ResolvePaths(t.TempDir()), t.TempDir()
	if err := os.MkdirAll(p.Mods, 0700); err != nil {
		t.Fatal(err)
	}
	writeModInfo(t, outside, `<xml><ModInfo><ID value="outside"/><Name value="Outside"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/></ModInfo></xml>`)
	makeReparseLink(t, outside, filepath.Join(p.Mods, "Escape"))
	if _, err := (ModService{paths: p}).SetEnabled("Escape", false); !errors.Is(err, ErrModPathEscape) {
		t.Fatalf("err=%v", err)
	}
}

func TestModPolicyStrictBlocksServerStartButWarnAllows(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeModInfo(t, filepath.Join(p.Mods, "Addon"), `<xml><ModInfo><ID value="addon"/><Name value="Addon"/><Version value="1.0"/><Author value="Author"/><Description value="Description"/><Dependencies><Dependency id="missing" version="1.0"/></Dependencies></ModInfo></xml>`)
	starts := 0
	launched := &fakeLaunchedProcess{pid: 42}
	server := newServerManager(p, nil, &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: p.ServerExe, Started: time.Unix(1, 0)}}, func(Paths) (launchedProcess, error) {
		starts++
		return launched, nil
	})
	service := ModService{paths: p, server: server}
	server.startCheck = service.StartBlocker
	if err := service.SetPolicy(DependencyStrict); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err == nil || starts != 0 || server.Status().State != ServerStopped {
		t.Fatalf("strict start err=%v starts=%d state=%q", err, starts, server.Status().State)
	}
	if err := service.SetPolicy(DependencyWarn); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil || starts != 1 {
		t.Fatalf("warn start err=%v starts=%d", err, starts)
	}
}

func TestModPolicyNormalizesStoredValueAndRejectsInput(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	if err := os.WriteFile(p.ModSettings, []byte(`{"listen":"127.0.0.1:8787","modDependencyPolicy":"unsafe"}`), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadPanelConfig(p.ModSettings)
	if err != nil || config.ModDependencyPolicy != DependencyWarn {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	if err := (ModService{paths: p}).SetPolicy(DependencyPolicy("unsafe")); err == nil {
		t.Fatal("invalid policy accepted")
	}
}

func writeModInfo(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ModInfo.xml"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func findMod(t *testing.T, catalog ModCatalog, id string) ModInfo {
	t.Helper()
	for _, mod := range catalog.Mods {
		if mod.ID == id {
			return mod
		}
	}
	t.Fatalf("mod %q not found in %#v", id, catalog.Mods)
	return ModInfo{}
}

func hasModProblem(mod ModInfo, kind string) bool {
	for _, problem := range mod.Problems {
		if problem.Kind == kind {
			return true
		}
	}
	return false
}

// The panel folder may be reached through a symlink, junction or 8.3 short
// name (as on Windows CI runners); mod moves must still work.
func TestModServiceWorksThroughAliasedPanelFolder(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "panel")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	p := ResolvePaths(alias)
	writeModInfo(t, filepath.Join(p.Mods, "Core"), `<xml><ModInfo><ID value="core"/><Name value="Core"/><Version value="1.0"/></ModInfo></xml>`)
	service := ModService{paths: p}
	if _, err := service.SetEnabled("Core", false); err != nil {
		t.Fatalf("disable through alias: %v", err)
	}
	if _, err := service.SetEnabled("Core", true); err != nil {
		t.Fatalf("enable through alias: %v", err)
	}
	if !exists(filepath.Join(real, "server", "Mods", "Core", "ModInfo.xml")) {
		t.Fatal("mod did not end up back in Mods")
	}
}

func zipFiles(t *testing.T, names ...string) []*zip.File {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range names {
		if strings.HasSuffix(name, "/") {
			header := &zip.FileHeader{Name: name}
			header.SetMode(os.ModeDir | 0o755)
			if _, err := w.CreateHeader(header); err != nil {
				t.Fatal(err)
			}
			continue
		}
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte("<xml/>"))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return r.File
}

func packageNames(pkg modImportPackage) []string {
	var names []string
	for _, entry := range pkg.entries {
		names = append(names, entry.name)
	}
	return names
}

func TestInspectModPackageAcceptsCommonLayouts(t *testing.T) {
	for _, test := range []struct {
		name, archive string
		files         []string
		roots         []string
		entries       []string
	}{
		{"canonical", "x.zip", []string{"A/ModInfo.xml", "A/Config/items.xml", "B/ModInfo.xml"}, []string{"A", "B"}, []string{"A/ModInfo.xml", "A/Config/items.xml", "B/ModInfo.xml"}},
		{"wrapper folder", "x.zip", []string{"Mods/", "Mods/readme.txt", "Mods/A/ModInfo.xml", "Mods/A/Config/items.xml"}, []string{"A"}, []string{"A/ModInfo.xml", "A/Config/items.xml"}},
		{"single mod folder", "x.zip", []string{"A/", "A/ModInfo.xml"}, []string{"A"}, []string{"A", "A/ModInfo.xml"}},
		{"files at root", "Better Bags-1.2.zip", []string{"ModInfo.xml", "Config/items.xml"}, []string{"Better Bags-1.2"}, []string{"Better Bags-1.2/ModInfo.xml", "Better Bags-1.2/Config/items.xml"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pkg, err := inspectModPackage(zipFiles(t, test.files...), strings.TrimSuffix(test.archive, ".zip"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(pkg.roots, ",") != strings.Join(test.roots, ",") || strings.Join(packageNames(pkg), ",") != strings.Join(test.entries, ",") {
				t.Fatalf("roots=%v entries=%v", pkg.roots, packageNames(pkg))
			}
		})
	}
}

func TestInspectModPackageRejectsUnrecognisedLayouts(t *testing.T) {
	for name, files := range map[string][]string{
		"no ModInfo":       {"A/Config/items.xml"},
		"wrapper without":  {"Mods/A/Config/items.xml"},
		"one of two roots": {"A/ModInfo.xml", "B/Config/items.xml"},
		"loose root file":  {"A/ModInfo.xml", "readme.txt"},
		"two levels deep":  {"X/Y/A/ModInfo.xml"},
		"traversal":        {"A/ModInfo.xml", "A/../../evil.dll"},
	} {
		if _, err := inspectModPackage(zipFiles(t, files...), "Fallback"); !errors.Is(err, ErrModPathEscape) {
			t.Errorf("%s: err=%v, want rejection", name, err)
		}
	}
	if _, err := inspectModPackage(zipFiles(t, "ModInfo.xml"), ".."); !errors.Is(err, ErrModPathEscape) {
		t.Errorf("root ModInfo with an unusable ZIP name: err=%v", err)
	}
}

func TestScanModsIgnoresLeftoverImportStages(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	for _, dir := range []string{filepath.Join(paths.Mods, ".mod-import-123", "Half"), filepath.Join(paths.Mods, "Real")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := ScanMods(paths, DependencyWarn)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Mods) != 1 || catalog.Mods[0].ID != "Real" {
		t.Fatalf("mods = %+v; a stage left by an interrupted import must not be listed", catalog.Mods)
	}
}

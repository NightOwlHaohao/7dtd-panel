package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveImportRejectsExcessiveEntryCountAndRequiresDiskHeadroom(t *testing.T) {
	entries := make([]*zip.File, maxImportEntries+1)
	if _, err := inspectSavePackage(entries); err == nil {
		t.Fatal("excessive save entry count was accepted")
	}
	if hasImportSpace((10<<30)-1, 9<<30) || !hasImportSpace(10<<30, 9<<30) {
		t.Fatal("import disk headroom boundary is wrong")
	}
}

func TestSwitchUpdatesOnlyWorldAndGame(t *testing.T) {
	p := saveFixture(t, "Navezgane", "newgame")
	doc, _ := LoadConfig(p.ServerConfig)
	hash, err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Switch("Navezgane", "newgame", doc.Hash)
	if err != nil || hash == doc.Hash {
		t.Fatalf("hash=%q err=%v", hash, err)
	}
	updated, _ := LoadConfig(p.ServerConfig)
	if value(updated, "GameWorld") != "Navezgane" || value(updated, "GameName") != "newgame" {
		t.Fatalf("config=%#v", updated)
	}
	if value(updated, "ServerName") != "unchanged" {
		t.Fatal("unrelated property changed")
	}
}

func TestDeleteExportsBeforeRemovingSave(t *testing.T) {
	p := saveFixture(t, "Navezgane", "oldgame")
	s := SaveService{paths: p, server: NewServerManager(p, nil)}
	backup, err := s.Delete(context.Background(), "Navezgane", "oldgame")
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(p.Saves, "Navezgane", "oldgame")) {
		t.Fatal("save still exists")
	}
	if !exists(filepath.Join(p.SavePackages, backup.Name)) {
		t.Fatal("pre-delete package missing")
	}
}

func TestDeleteKeepsSaveWhenBackupPayloadIsCorrupt(t *testing.T) {
	p := saveFixture(t, "Navezgane", "oldgame")
	s := SaveService{paths: p, server: NewServerManager(p, nil)}
	s.rename = func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		if strings.HasSuffix(to, ".zip") {
			corruptZipPayload(t, to, "save/main.ttw")
		}
		return nil
	}
	if _, err := s.Delete(context.Background(), "Navezgane", "oldgame"); err == nil {
		t.Fatal("expected corrupt backup error")
	}
	if string(mustRead(t, filepath.Join(p.Saves, "Navezgane", "oldgame", "main.ttw"))) != "original" {
		t.Fatal("save was deleted after corrupt backup")
	}
}

func TestImportRejectsTraversalAndLeavesTargetsUntouched(t *testing.T) {
	p := saveFixture(t, "Navezgane", "existing")
	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldBuiltin, World: "Navezgane", Game: "existing"}, map[string]string{"save/../../escape": "bad"})
	err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Import(context.Background(), archive)
	if !errors.Is(err, ErrUnsafeSavePath) {
		t.Fatalf("err=%v", err)
	}
	if string(mustRead(t, filepath.Join(p.Saves, "Navezgane", "existing", "main.ttw"))) != "original" {
		t.Fatal("target changed")
	}
}

func TestImportReplacementBacksUpAndRollsBackOnRenameFailure(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	worldTarget := filepath.Join(p.GeneratedWorlds, "EastCoast_8K")
	saveTarget := filepath.Join(p.Saves, "EastCoast_8K", "survival")
	writeFile(t, filepath.Join(worldTarget, "map_info.xml"), "original-world")
	writeFile(t, filepath.Join(saveTarget, "main.ttw"), "original-save")
	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldGenerated, World: "EastCoast_8K", Game: "survival"}, map[string]string{
		"world/map_info.xml": "replacement-world",
		"save/main.ttw":      "replacement-save",
	})
	boom := errors.New("rename failed")
	finalRenames := 0
	s := SaveService{paths: p, server: NewServerManager(p, nil)}
	s.rename = func(from, to string) error {
		if filepath.Clean(to) == filepath.Clean(worldTarget) || filepath.Clean(to) == filepath.Clean(saveTarget) {
			finalRenames++
			if finalRenames == 2 {
				return boom
			}
		}
		return os.Rename(from, to)
	}
	if err := s.Import(context.Background(), archive); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if string(mustRead(t, filepath.Join(worldTarget, "map_info.xml"))) != "original-world" {
		t.Fatal("world not restored")
	}
	if string(mustRead(t, filepath.Join(saveTarget, "main.ttw"))) != "original-save" {
		t.Fatal("save not restored")
	}
	packages, _ := filepath.Glob(filepath.Join(p.SavePackages, "*.zip"))
	if len(packages) != 1 {
		t.Fatalf("pre-import packages=%v", packages)
	}
}

func TestImportKeepsTargetWhenPreImportBackupIsInvalid(t *testing.T) {
	p := saveFixture(t, "Navezgane", "existing")
	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldBuiltin, World: "Navezgane", Game: "existing"}, map[string]string{"save/main.ttw": "replacement"})
	s := SaveService{paths: p, server: NewServerManager(p, nil)}
	s.rename = func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		if strings.HasSuffix(to, ".zip") {
			corruptZipPayload(t, to, "save/main.ttw")
		}
		return nil
	}
	if err := s.Import(context.Background(), archive); err == nil {
		t.Fatal("expected invalid backup error")
	}
	if string(mustRead(t, filepath.Join(p.Saves, "Navezgane", "existing", "main.ttw"))) != "original" {
		t.Fatal("target changed after invalid backup")
	}
}

func TestImportRollbackPreservesBothErrorsWhenRecoveryMoveFails(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	worldTarget := filepath.Join(p.GeneratedWorlds, "EastCoast_8K")
	saveTarget := filepath.Join(p.Saves, "EastCoast_8K", "survival")
	writeFile(t, filepath.Join(worldTarget, "map_info.xml"), "original-world")
	writeFile(t, filepath.Join(saveTarget, "main.ttw"), "original-save")
	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldGenerated, World: "EastCoast_8K", Game: "survival"}, map[string]string{
		"world/map_info.xml": "replacement-world",
		"save/main.ttw":      "replacement-save",
	})
	finalBoom, recoveryBoom := errors.New("final rename failed"), errors.New("recovery move failed")
	finalRenames := 0
	s := SaveService{paths: p, server: NewServerManager(p, nil)}
	s.rename = func(from, to string) error {
		if filepath.Clean(to) == filepath.Clean(worldTarget) || filepath.Clean(to) == filepath.Clean(saveTarget) {
			finalRenames++
			if finalRenames == 2 {
				return finalBoom
			}
		}
		if filepath.Clean(from) == filepath.Clean(worldTarget) && strings.Contains(filepath.Base(filepath.Dir(to)), ".import-") {
			return recoveryBoom
		}
		return os.Rename(from, to)
	}
	err := s.Import(context.Background(), archive)
	if !errors.Is(err, finalBoom) || !errors.Is(err, recoveryBoom) {
		t.Fatalf("err=%v", err)
	}
	if string(mustRead(t, filepath.Join(worldTarget, "map_info.xml"))) != "replacement-world" {
		t.Fatal("replacement was not preserved for recovery")
	}
	previous, err := filepath.Glob(filepath.Join(p.GeneratedWorlds, ".previous-*", "map_info.xml"))
	if err != nil || len(previous) != 1 || string(mustRead(t, previous[0])) != "original-world" {
		t.Fatalf("original world was not preserved: %v", previous)
	}
}

func saveFixture(t *testing.T, world, game string) Paths {
	t.Helper()
	p := ResolvePaths(t.TempDir())
	mkdirs(t, filepath.Join(p.BuiltinWorlds, world))
	writeFile(t, filepath.Join(p.Saves, world, game, "main.ttw"), "original")
	config := `<ServerSettings><property name="ServerName" value="unchanged"/><property name="GameWorld" value="OldWorld"/><property name="GameName" value="oldgame"/></ServerSettings>`
	writeFile(t, p.ServerConfig, config)
	return p
}

func value(doc ConfigDocument, name string) string {
	got, _ := configValue(doc, name)
	return got
}

func makePackage(t *testing.T, manifest SavePackageManifest, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	manifestBytes, _ := json.Marshal(manifest)
	w, _ := zw.Create("manifest.json")
	_, _ = w.Write(manifestBytes)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func corruptZipPayload(t *testing.T, path, name string) {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	var offset int64 = -1
	for _, file := range archive.File {
		if file.Name == name {
			offset, err = file.DataOffset()
			break
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err != nil || offset < 0 {
		t.Fatalf("payload %q offset=%d err=%v", name, offset, err)
	}
	b := mustRead(t, path)
	b[offset] ^= 0xff
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExportGeneratedWorldPackagesWorldAndSave(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeFile(t, filepath.Join(p.GeneratedWorlds, "EastCoast_8K", "map_info.xml"), "map")
	writeFile(t, filepath.Join(p.Saves, "EastCoast_8K", "survival", "main.ttw"), "save")
	info, err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Export(context.Background(), "EastCoast_8K", "survival")
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, filepath.Join(p.SavePackages, info.Name))
	for _, want := range []string{"manifest.json", "world/map_info.xml", "save/main.ttw"} {
		if !names[want] {
			t.Errorf("missing %s", want)
		}
	}
}

func TestExportBuiltinWorldOmitsWorldFiles(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	mkdirs(t, filepath.Join(p.BuiltinWorlds, "Navezgane"))
	writeFile(t, filepath.Join(p.Saves, "Navezgane", "game", "main.ttw"), "save")
	info, err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Export(context.Background(), "Navezgane", "game")
	if err != nil {
		t.Fatal(err)
	}
	for name := range zipNames(t, filepath.Join(p.SavePackages, info.Name)) {
		if strings.HasPrefix(name, "world/") {
			t.Fatalf("builtin world included: %s", name)
		}
	}
}

func TestExportRejectsReparsePackageAncestorBeforeWriting(t *testing.T) {
	p, outside := saveFixture(t, "Navezgane", "game"), t.TempDir()
	makeReparseLink(t, outside, p.Backups)
	if _, err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Export(context.Background(), "Navezgane", "game"); !errors.Is(err, ErrUnsafeSavePath) {
		t.Fatalf("err=%v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside entries=%v err=%v", entries, err)
	}
}

func TestExportRequiresStoppedServer(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeFile(t, filepath.Join(p.Saves, "Navezgane", "game", "main.ttw"), "save")
	started := time.Unix(10, 0)
	server := newServerManager(p, nil, &fakeProcessProbe{identity: ProcessIdentity{PID: 1, Exe: p.ServerExe, Started: started}}, nil)
	writeRecordedProcess(t, p, persistedProcess{PID: 1, Exe: p.ServerExe, Started: started})
	_, err := (SaveService{paths: p, server: server}).Export(context.Background(), "Navezgane", "game")
	if !errors.Is(err, ErrServerMustBeStopped) {
		t.Fatalf("err=%v", err)
	}
}

func TestExportRejectsIrregularFile(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	save := filepath.Join(p.Saves, "Navezgane", "game")
	mkdirs(t, save)
	if err := os.Symlink(t.TempDir(), filepath.Join(save, "linked")); err != nil {
		t.Skip(err)
	}
	_, err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Export(context.Background(), "Navezgane", "game")
	if !errors.Is(err, ErrUnsafeSavePath) {
		t.Fatalf("err=%v", err)
	}
}

func TestExportRejectsReparseSaveAncestors(t *testing.T) {
	for _, test := range []struct {
		name    string
		link    func(Paths) string
		outside string
	}{
		{"saves root", func(p Paths) string { return p.Saves }, filepath.Join("Navezgane", "game", "main.ttw")},
		{"world root", func(p Paths) string { return filepath.Join(p.Saves, "Navezgane") }, filepath.Join("game", "main.ttw")},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, outside := ResolvePaths(t.TempDir()), t.TempDir()
			writeFile(t, filepath.Join(outside, test.outside), "outside")
			mkdirs(t, filepath.Dir(test.link(p)))
			makeReparseLink(t, outside, test.link(p))
			_, err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Export(context.Background(), "Navezgane", "game")
			if !errors.Is(err, ErrUnsafeSavePath) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestExportIgnoresIrregularUnrelatedSave(t *testing.T) {
	p, outside := ResolvePaths(t.TempDir()), t.TempDir()
	writeFile(t, filepath.Join(p.Saves, "Navezgane", "game", "main.ttw"), "selected")
	writeFile(t, filepath.Join(p.Saves, "Other", "bad", "main.ttw"), "unrelated")
	makeReparseLink(t, outside, filepath.Join(p.Saves, "Other", "bad", "linked"))
	info, err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Export(context.Background(), "Navezgane", "game")
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, filepath.Join(p.SavePackages, info.Name))
	if !names["save/main.ttw"] || names["save/linked"] || names["save/../Other/bad/main.ttw"] {
		t.Fatalf("names=%#v", names)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func zipNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	names := make(map[string]bool, len(r.File))
	for _, file := range r.File {
		names[file.Name] = true
	}
	return names
}

func TestSaveCatalogGroupsByWorldAndMarksCurrentAndMissing(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	mkdirs(t,
		filepath.Join(p.BuiltinWorlds, "Navezgane"),
		filepath.Join(p.GeneratedWorlds, "EastCoast_8K"),
		filepath.Join(p.Saves, "Navezgane", "美东七日杀中文服"),
		filepath.Join(p.Saves, "MissingWorld", "orphan"),
	)
	got, err := (SaveService{paths: p}).Catalog("Navezgane", "美东七日杀中文服")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Worlds) != 3 {
		t.Fatalf("worlds=%#v", got.Worlds)
	}
	current := findSave(t, got, "Navezgane", "美东七日杀中文服")
	if !current.Active {
		t.Fatal("current save not marked active")
	}
	missing := findWorld(t, got, "MissingWorld")
	if !missing.Missing || missing.Kind != WorldMissing {
		t.Fatalf("missing=%#v", missing)
	}
}

func TestSaveCatalogRejectsReparseEntries(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	mkdirs(t, p.Saves)
	outside := t.TempDir()
	link := filepath.Join(p.Saves, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	got, err := (SaveService{paths: p}).Catalog("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Worlds) != 0 {
		t.Fatalf("followed link: %#v", got.Worlds)
	}
}

func TestSaveCatalogRejectsReparseRoots(t *testing.T) {
	for _, test := range []struct {
		name string
		root func(Paths) string
	}{
		{"builtin worlds", func(p Paths) string { return p.BuiltinWorlds }},
		{"generated worlds", func(p Paths) string { return p.GeneratedWorlds }},
		{"saves", func(p Paths) string { return p.Saves }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, outside := ResolvePaths(t.TempDir()), t.TempDir()
			mkdirs(t, filepath.Join(outside, "OutsideWorld", "game"), filepath.Dir(test.root(p)))
			makeReparseLink(t, outside, test.root(p))
			got, err := (SaveService{paths: p}).Catalog("", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Worlds) != 0 {
				t.Fatalf("followed reparse root: %#v", got.Worlds)
			}
		})
	}
}

func TestSaveCatalogClassifiesAndSortsCaseInsensitively(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	mkdirs(t,
		filepath.Join(p.BuiltinWorlds, "Navezgane"),
		filepath.Join(p.BuiltinWorlds, "Pregen01"),
		filepath.Join(p.BuiltinWorlds, "BravoBuiltin"),
		filepath.Join(p.GeneratedWorlds, "alphaGenerated"),
		filepath.Join(p.Saves, "Navezgane", "BravoSave"),
		filepath.Join(p.Saves, "Navezgane", "alphaSave"),
	)
	got, err := (SaveService{paths: p}).Catalog("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Worlds) != 4 {
		t.Fatalf("worlds=%#v", got.Worlds)
	}
	for i, want := range []string{"alphaGenerated", "BravoBuiltin", "Navezgane", "Pregen01"} {
		if got.Worlds[i].Name != want {
			t.Fatalf("worlds=%#v", got.Worlds)
		}
	}
	for name, kind := range map[string]WorldKind{
		"alphaGenerated": WorldGenerated,
		"BravoBuiltin":   WorldBuiltin,
		"Navezgane":      WorldOfficial,
		"Pregen01":       WorldPregen,
	} {
		if got := findWorld(t, got, name).Kind; got != kind {
			t.Fatalf("%s kind=%q want %q", name, got, kind)
		}
	}
	saves := findWorld(t, got, "Navezgane").Saves
	if saves[0].Name != "alphaSave" || saves[1].Name != "BravoSave" {
		t.Fatalf("saves=%#v", saves)
	}
}

func TestSaveCatalogKeepsReservedNamesGeneratedOutsideBuiltinRoot(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	mkdirs(t,
		filepath.Join(p.GeneratedWorlds, "Navezgane"),
		filepath.Join(p.GeneratedWorlds, "Pregen08k1"),
	)
	catalog := mustCatalog(t, p)
	for _, name := range []string{"Navezgane", "Pregen08k1"} {
		if got := findWorld(t, catalog, name).Kind; got != WorldGenerated {
			t.Fatalf("%s kind=%q want %q", name, got, WorldGenerated)
		}
	}
}

func TestSaveCatalogReportsRegularFileStats(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	path := filepath.Join(p.Saves, "Navezgane", "game", "nested", "main.ttw")
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("save"), 0600); err != nil {
		t.Fatal(err)
	}
	modified := time.Now().Add(time.Hour).Round(time.Second)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	save := findSave(t, mustCatalog(t, p), "Navezgane", "game")
	if save.Size != 4 || !save.Modified.Equal(modified) {
		t.Fatalf("save=%#v", save)
	}
}

func TestSaveCatalogRejectsNestedReparseEntries(t *testing.T) {
	p, outside := ResolvePaths(t.TempDir()), t.TempDir()
	file := filepath.Join(p.Saves, "Navezgane", "game", "main.ttw")
	mkdirs(t, filepath.Dir(file))
	if err := os.WriteFile(file, []byte("save"), 0600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "escape.ttw")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	makeReparseLink(t, outside, filepath.Join(filepath.Dir(file), "linked"))
	if save := findSave(t, mustCatalog(t, p), "Navezgane", "game"); save.Size != 4 {
		t.Fatalf("followed nested reparse entry: %#v", save)
	}
}

func TestSafeSaveComponentRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "one/two", `one\\two`, `C:\\save`, "save\x00name"} {
		if err := safeSaveComponent(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if err := safeSaveComponent("七日杀中文服"); err != nil {
		t.Fatal(err)
	}
}

func mustCatalog(t *testing.T, p Paths) SaveCatalog {
	t.Helper()
	catalog, err := (SaveService{paths: p}).Catalog("", "")
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func makeReparseLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if err := exec.Command("cmd", "/c", "mklink", "/J", link, target).Run(); err != nil {
		t.Skip(err)
	}
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
}

func findWorld(t *testing.T, catalog SaveCatalog, name string) WorldInfo {
	t.Helper()
	for _, world := range catalog.Worlds {
		if world.Name == name {
			return world
		}
	}
	t.Fatalf("world %q not found", name)
	return WorldInfo{}
}

func findSave(t *testing.T, catalog SaveCatalog, world, game string) SaveInfo {
	t.Helper()
	for _, save := range findWorld(t, catalog, world).Saves {
		if save.Name == game {
			return save
		}
	}
	t.Fatalf("save %q/%q not found", world, game)
	return SaveInfo{}
}

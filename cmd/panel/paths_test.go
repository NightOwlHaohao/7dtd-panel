package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolvePathsMovesAsOneRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses a Windows drive path")
	}
	p := ResolvePaths(filepath.Join("X:\\", "SevenPanel"))
	if p.ServerExe != filepath.Join("X:\\SevenPanel", "server", "7DaysToDieServer.exe") {
		t.Fatalf("unexpected server exe: %s", p.ServerExe)
	}
	if p.UserData != filepath.Join("X:\\SevenPanel", "userdata") {
		t.Fatalf("unexpected userdata: %s", p.UserData)
	}
	if filepath.Dir(p.UserData) != p.Root {
		t.Fatal("userdata must be outside server directory")
	}
}

func TestPanelConfigRejectsNonLoopback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.json")
	if err := os.WriteFile(path, []byte(`{"listen":"0.0.0.0:8787"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPanelConfig(path); err == nil {
		t.Fatal("expected non-loopback listen address to be rejected")
	}
}

func TestLoadPanelConfigCreatesLoopbackDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.json")
	config, err := LoadPanelConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:8787" {
		t.Fatalf("unexpected default listen address: %s", config.Listen)
	}
}

func TestPanelConfigPersistsFirewallSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.json")
	want := PanelConfig{Listen: "127.0.0.1:8787", Firewall: FirewallSettings{Custom: []FirewallRule{{Name: "range", Protocol: "TCP", Ports: "1-65535", Profile: "Private", Enabled: true}}}}
	if err := savePanelConfig(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPanelConfig(path)
	if err != nil || len(got.Firewall.Custom) != 1 || got.Firewall.Custom[0].Ports != "1-65535" {
		t.Fatalf("config=%#v err=%v", got, err)
	}
}

func TestUpdatePanelConfigPreservesUnrelatedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.json")
	if _, err := updatePanelConfig(path, func(config *PanelConfig) { config.Firewall.Dashboard = true }); err != nil {
		t.Fatal(err)
	}
	if _, err := updatePanelConfig(path, func(config *PanelConfig) { config.ModDependencyPolicy = DependencyStrict }); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPanelConfig(path)
	if err != nil || !got.Firewall.Dashboard || got.ModDependencyPolicy != DependencyStrict {
		t.Fatalf("config=%#v err=%v", got, err)
	}
}

func TestPrepareDataDirsDoesNotCreateServer(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	if err := p.PrepareDataDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.ServerDir); !os.IsNotExist(err) {
		t.Fatalf("server directory should not be created: %v", err)
	}
	for _, path := range []string{p.UserData, p.ConfigBackups, p.UserDataBackups, p.Cache, p.Logs, p.SteamCMDDir} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("data directory %s was not created: %v", path, err)
		}
	}
	if _, err := os.Stat(p.SavePackages); !os.IsNotExist(err) {
		t.Fatalf("save package directory should be created by the safe package writer: %v", err)
	}
	for _, path := range []string{p.BuiltinWorlds, p.GeneratedWorlds, p.Saves} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("game directory %s should not be created: %v", path, err)
		}
	}
}

func TestResolvePathsIncludesWorldAndSaveRoots(t *testing.T) {
	root := t.TempDir()
	p := ResolvePaths(root)
	for got, want := range map[string]string{
		p.BuiltinWorlds:   filepath.Join(root, "server", "Data", "Worlds"),
		p.GeneratedWorlds: filepath.Join(root, "userdata", "GeneratedWorlds"),
		p.Saves:           filepath.Join(root, "userdata", "Saves"),
		p.SavePackages:    filepath.Join(root, "backups", "saves"),
		p.LocalizationCSV: filepath.Join(root, "server", "Data", "Config", "Localization.csv"),
		p.Mods:            filepath.Join(root, "server", "Mods"),
		p.DisabledMods:    filepath.Join(root, "server", "Mods.disabled"),
		p.ModSettings:     filepath.Join(root, "panel.json"),
	} {
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestInspectReportsMissingSetup(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	status := p.Inspect()
	if status.ServerExe || status.ServerConfig || status.UserData || status.SteamCMD {
		t.Fatalf("unexpected setup status: %+v", status)
	}
	if len(status.Problems) != 4 {
		t.Fatalf("unexpected problems: %+v", status.Problems)
	}
}

func TestInspectReturnsEmptyProblemsForValidSetup(t *testing.T) {
	p := ResolvePaths(t.TempDir())
	writeCompleteSetup(t, p, strings.ToUpper(p.UserData))
	status := p.Inspect()
	if status.Problems == nil || len(status.Problems) != 0 {
		t.Fatalf("problems must be []: %#v", status.Problems)
	}
}

func TestInspectValidatesServerConfigUserDataFolder(t *testing.T) {
	for _, test := range []struct {
		name    string
		xml     string
		problem string
	}{
		{"invalid xml", `<ServerSettings>`, "格式"},
		{"missing property", `<ServerSettings></ServerSettings>`, "缺少"},
		{"wrong path", `<ServerSettings><property name="UserDataFolder" value="C:\\wrong"/></ServerSettings>`, "未指向"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := ResolvePaths(t.TempDir())
			writeCompleteSetup(t, p, p.UserData)
			if err := os.WriteFile(p.ServerConfig, []byte(test.xml), 0600); err != nil {
				t.Fatal(err)
			}
			status := p.Inspect()
			if len(status.Problems) != 1 || !strings.Contains(status.Problems[0], test.problem) {
				t.Fatalf("problems=%#v", status.Problems)
			}
		})
	}
}

func writeCompleteSetup(t *testing.T, p Paths, userData string) {
	t.Helper()
	for _, dir := range []string{p.ServerDir, p.UserData, p.SteamCMDDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{p.ServerExe, p.SteamCMD} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	xml := fmt.Sprintf(`<ServerSettings><property name="UserDataFolder" value="%s"/><property name="TelnetPassword" value="top-secret"/></ServerSettings>`, userData)
	if err := os.WriteFile(p.ServerConfig, []byte(xml), 0600); err != nil {
		t.Fatal(err)
	}
}

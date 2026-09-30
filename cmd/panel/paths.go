package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Paths struct {
	Root, PanelConfig, ServerDir, ServerExe, ServerConfig, LocalizationCSV string
	UserData, Backups, ConfigBackups, UserDataBackups                      string
	Cache, SandboxCache, Logs, SteamCMDDir, SteamCMD                       string
	BuiltinWorlds, GeneratedWorlds, Saves, SavePackages                    string
	Mods, DisabledMods, ModSettings                                        string
}

type PanelConfig struct {
	Listen              string           `json:"listen"`
	ModDependencyPolicy DependencyPolicy `json:"modDependencyPolicy"`
	Firewall            FirewallSettings `json:"firewall"`
	Schedule            ScheduleSettings `json:"schedule"`
	Steam               SteamSettings    `json:"steam"`
	NoUpdateCheck       bool             `json:"noUpdateCheck,omitempty"`
	NaiwaziPort         int              `json:"naiwaziPort,omitempty"`
}

var panelConfigMu sync.Mutex

type SetupStatus struct {
	ServerExe    bool     `json:"serverExe"`
	ServerConfig bool     `json:"serverConfig"`
	UserData     bool     `json:"userData"`
	SteamCMD     bool     `json:"steamCmd"`
	Problems     []string `json:"problems"`
}

func ResolvePaths(root string) Paths {
	return Paths{
		Root:            root,
		PanelConfig:     filepath.Join(root, "panel.json"),
		ServerDir:       filepath.Join(root, "server"),
		ServerExe:       filepath.Join(root, "server", "7DaysToDieServer.exe"),
		ServerConfig:    filepath.Join(root, "server", "serverconfig.xml"),
		LocalizationCSV: filepath.Join(root, "server", "Data", "Config", "Localization.csv"),
		UserData:        filepath.Join(root, "userdata"),
		Backups:         filepath.Join(root, "backups"),
		ConfigBackups:   filepath.Join(root, "backups", "config"),
		UserDataBackups: filepath.Join(root, "backups", "userdata"),
		BuiltinWorlds:   filepath.Join(root, "server", "Data", "Worlds"),
		GeneratedWorlds: filepath.Join(root, "userdata", "GeneratedWorlds"),
		Saves:           filepath.Join(root, "userdata", "Saves"),
		SavePackages:    filepath.Join(root, "backups", "saves"),
		Mods:            filepath.Join(root, "server", "Mods"),
		DisabledMods:    filepath.Join(root, "server", "Mods.disabled"),
		ModSettings:     filepath.Join(root, "panel.json"),
		Cache:           filepath.Join(root, "cache"),
		SandboxCache:    filepath.Join(root, "cache", "sandbox-schema.json"),
		Logs:            filepath.Join(root, "logs"),
		SteamCMDDir:     filepath.Join(root, "tools", "steamcmd"),
		SteamCMD:        filepath.Join(root, "tools", "steamcmd", "steamcmd.exe"),
	}
}

func LoadPanelConfig(path string) (PanelConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		config := PanelConfig{Listen: "127.0.0.1:8787"}
		if err := writePanelConfig(path, config); err != nil {
			return PanelConfig{}, err
		}
		data, err = json.Marshal(config)
		if err != nil {
			return PanelConfig{}, err
		}
	} else if err != nil {
		return PanelConfig{}, err
	}

	var config PanelConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return PanelConfig{}, err
	}
	if !validDependencyPolicy(config.ModDependencyPolicy) {
		config.ModDependencyPolicy = DependencyWarn
	}
	host, port, err := net.SplitHostPort(config.Listen)
	if err != nil || port == "" || (host != "127.0.0.1" && host != "localhost") {
		return PanelConfig{}, fmt.Errorf("panel listen address must be 127.0.0.1 or localhost with a port")
	}
	return config, nil
}

func savePanelConfig(path string, config PanelConfig) error {
	panelConfigMu.Lock()
	defer panelConfigMu.Unlock()
	return writePanelConfig(path, config)
}

func updatePanelConfig(path string, update func(*PanelConfig)) (PanelConfig, error) {
	panelConfigMu.Lock()
	defer panelConfigMu.Unlock()
	config, err := LoadPanelConfig(path)
	if err != nil {
		return PanelConfig{}, err
	}
	update(&config)
	return config, writePanelConfig(path, config)
}

func writePanelConfig(path string, config PanelConfig) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".panel-*.json")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func (p Paths) Inspect() SetupStatus {
	status := SetupStatus{
		ServerExe:    exists(p.ServerExe),
		ServerConfig: exists(p.ServerConfig),
		UserData:     exists(p.UserData),
		SteamCMD:     exists(p.SteamCMD),
		Problems:     []string{},
	}
	for _, item := range []struct {
		ok   bool
		name string
	}{
		{status.ServerExe, "server executable"},
		{status.ServerConfig, "server config"},
		{status.UserData, "userdata"},
		{status.SteamCMD, "steamcmd"},
	} {
		if !item.ok {
			status.Problems = append(status.Problems, "missing "+item.name)
		}
	}
	if status.ServerConfig {
		doc, err := LoadConfig(p.ServerConfig)
		if err != nil {
			status.Problems = append(status.Problems, "serverconfig.xml 无法读取或格式错误")
			return status
		}
		configured, ok := configValue(doc, "UserDataFolder")
		expected, absErr := filepath.Abs(p.UserData)
		if !ok {
			status.Problems = append(status.Problems, "serverconfig.xml 缺少 UserDataFolder")
		} else if absErr != nil || !strings.EqualFold(filepath.Clean(configured), filepath.Clean(expected)) {
			status.Problems = append(status.Problems, "UserDataFolder 未指向当前 userdata 目录")
		}
	}
	return status
}

func (p Paths) PrepareDataDirs() error {
	for _, dir := range []string{p.UserData, p.ConfigBackups, p.UserDataBackups, p.Cache, p.Logs, p.SteamCMDDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

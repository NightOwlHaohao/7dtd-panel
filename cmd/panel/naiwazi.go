package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// NaiwaziBot (https://www.7risha.com/7038.html) is a popular server-side
// mod that adds its own web panel (players, points shop, teleports…) on
// the game port + 6 (docs: http://cn.naiwazi.com/bot/). It is free to use but
// not open source — github.com/Naiwazi/NaiwaziBot holds only a README, the
// release is obfuscated .NET with no licence — so it is not bundled: the panel only
// detects an installed copy, tells where its web panel and password are,
// and links to it. It is installed like any mod (Mod page, ZIP upload).

const naiwaziModName = "NaiwaziBot"

type NaiwaziStatus struct {
	Installed    bool   `json:"installed"`
	Enabled      bool   `json:"enabled"`
	Version      string `json:"version,omitempty"`
	Folder       string `json:"folder,omitempty"`
	Port         int    `json:"port"`
	PortFromGame bool   `json:"portFromGame"` // game port + 6, not set by hand
	Listening    bool   `json:"listening"`
	URL          string `json:"url,omitempty"`
	PasswordFile string `json:"passwordFile,omitempty"`
}

// naiwaziPasswordFiles are where NaiwaziBot writes its web login.
func naiwaziPasswordFiles(paths Paths) []string {
	return []string{
		filepath.Join(paths.ServerDir, "NaiwaziBot_Data", "NaiwaziBot_Password.txt"),
		filepath.Join(filepath.Dir(adminFilePath(paths)), "NaiwaziBot_Password.txt"),
	}
}

func gamePort(paths Paths) int {
	if doc, err := LoadConfig(paths.ServerConfig); err == nil {
		if value, ok := configValue(doc, "ServerPort"); ok {
			if port, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && port > 0 && port < 65530 {
				return port
			}
		}
	}
	return 26900
}

func naiwaziStatus(paths Paths, override int, dial func(address string) bool) NaiwaziStatus {
	status := NaiwaziStatus{Port: override}
	if status.Port == 0 {
		status.Port, status.PortFromGame = gamePort(paths)+6, true
	}
	if catalog, err := ScanMods(paths, DependencyWarn); err == nil {
		for _, mod := range catalog.Mods {
			if strings.EqualFold(mod.ID, naiwaziModName) || strings.EqualFold(mod.Name, naiwaziModName) || strings.EqualFold(filepath.Base(mod.Path), naiwaziModName) {
				status.Installed, status.Version, status.Folder = true, mod.Version, mod.Path
				status.Enabled = status.Enabled || mod.Enabled
			}
		}
	}
	for _, path := range naiwaziPasswordFiles(paths) {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			status.PasswordFile = path
			break
		}
	}
	if status.Installed {
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(status.Port))
		status.URL = "http://" + address + "/"
		if dial != nil {
			status.Listening = dial(address)
		}
	}
	return status
}

func dialLocal(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// readNaiwaziPassword returns the login file's text, which NaiwaziBot
// writes in plain text next to the server.
func readNaiwaziPassword(paths Paths) (string, string, error) {
	for _, path := range naiwaziPasswordFiles(paths) {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > 4096 {
			return "", path, errors.New("the password file is unexpectedly large")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", path, err
		}
		return strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff")), path, nil
	}
	return "", "", os.ErrNotExist
}

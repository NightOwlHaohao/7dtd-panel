package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const panelReleaseURL = "https://api.github.com/repos/NightOwlHaohao/7dtd-panel/releases/latest"

const (
	panelCheckInterval = 24 * time.Hour
	gameCheckInterval  = 6 * time.Hour
)

type PanelVersion struct {
	Current         string     `json:"current"`
	Latest          string     `json:"latest,omitempty"`
	URL             string     `json:"url,omitempty"`
	CheckedAt       *time.Time `json:"checkedAt,omitempty"`
	Error           string     `json:"error,omitempty"`
	UpdateAvailable bool       `json:"updateAvailable"`
}

type GameVersion struct {
	Installed       InstalledGame  `json:"installed"`
	Branch          string         `json:"branch"` // the branch updates install
	Latest          string         `json:"latest,omitempty"`
	Branches        []RemoteBranch `json:"branches,omitempty"`
	CheckedAt       *time.Time     `json:"checkedAt,omitempty"`
	Error           string         `json:"error,omitempty"`
	UpdateAvailable bool           `json:"updateAvailable"`
}

type SteamSettingsView struct {
	Branch      string `json:"branch"`
	HasPassword bool   `json:"hasPassword"`
	AutoCheck   bool   `json:"autoCheck"`
}

type VersionReport struct {
	Panel PanelVersion      `json:"panel"`
	Game  GameVersion       `json:"game"`
	Steam SteamSettingsView `json:"steam"`
}

// VersionChecker remembers the last panel release and game build checks.
type VersionChecker struct {
	mu         sync.Mutex
	client     *http.Client
	releaseURL string
	current    string
	panel      PanelVersion
	branches   []RemoteBranch
	gameAt     time.Time
	gameErr    string
	announced  map[string]bool
}

func NewVersionChecker(current string) *VersionChecker {
	return &VersionChecker{client: &http.Client{Timeout: 20 * time.Second}, releaseURL: panelReleaseURL, current: current, panel: PanelVersion{Current: current}, announced: map[string]bool{}}
}

var semverPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// newerVersion reports whether latest is a higher vX.Y.Z than current.
// Development builds ("dev") never report an update.
func newerVersion(latest, current string) bool {
	l, c := semverPattern.FindStringSubmatch(latest), semverPattern.FindStringSubmatch(current)
	if l == nil || c == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		a, _ := strconv.Atoi(l[i])
		b, _ := strconv.Atoi(c[i])
		if a != b {
			return a > b
		}
	}
	return false
}

// CheckPanel asks GitHub for the latest release.
func (v *VersionChecker) CheckPanel(ctx context.Context) PanelVersion {
	result := PanelVersion{Current: v.current}
	now := time.Now()
	result.CheckedAt = &now
	latest, url, err := v.fetchRelease(ctx)
	if err != nil {
		result.Error = sanitizedError(err)
	} else {
		result.Latest, result.URL = latest, url
		result.UpdateAvailable = newerVersion(latest, v.current)
	}
	v.mu.Lock()
	v.panel = result
	v.mu.Unlock()
	return result
}

func (v *VersionChecker) fetchRelease(ctx context.Context) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.releaseURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "7dtd-panel/"+v.current)
	resp, err := v.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", errors.New("no public release found (the repository may be private)")
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", "", err
	}
	if release.TagName == "" {
		return "", "", errors.New("the latest release has no tag")
	}
	return release.TagName, release.HTMLURL, nil
}

func (v *VersionChecker) recordGame(branches []RemoteBranch, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.gameAt = time.Now()
	if err != nil {
		v.gameErr = sanitizedError(err)
		return
	}
	v.branches, v.gameErr = branches, ""
}

// Report combines the last checks with what is installed now.
func (v *VersionChecker) Report(installed InstalledGame, steam SteamSettings, autoCheck bool) VersionReport {
	v.mu.Lock()
	defer v.mu.Unlock()
	game := GameVersion{Installed: installed, Branch: steam.Branch, Branches: append([]RemoteBranch(nil), v.branches...), Error: v.gameErr}
	if game.Branch == "" {
		game.Branch = installed.Branch
	}
	if game.Branch == "" {
		game.Branch = "public"
	}
	if !v.gameAt.IsZero() {
		at := v.gameAt
		game.CheckedAt = &at
	}
	for _, branch := range v.branches {
		if branch.Name == game.Branch {
			game.Latest = branch.BuildID
		}
	}
	// A different branch than installed, or a newer build, means an update
	// would change the game.
	game.UpdateAvailable = installed.Installed && game.Latest != "" && (game.Latest != installed.BuildID || game.Branch != installed.Branch)
	return VersionReport{Panel: v.panel, Game: game, Steam: SteamSettingsView{Branch: steam.Branch, HasPassword: steam.BranchPassword != "", AutoCheck: autoCheck}}
}

// announce returns true the first time an update to key is seen.
func (v *VersionChecker) announce(key string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.announced[key] {
		return false
	}
	v.announced[key] = true
	return true
}

func (v *VersionChecker) due() (panel, game bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	panel = v.panel.CheckedAt == nil || time.Since(*v.panel.CheckedAt) >= panelCheckInterval
	game = v.gameAt.IsZero() || time.Since(v.gameAt) >= gameCheckInterval
	return panel, game
}

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleManifest = `"AppState"
{
	"appid"		"294420"
	"name"		"7 Days to Die Dedicated Server"
	"buildid"		"19571234"
	"InstalledDepots"
	{
		"294422"
		{
			"manifest"		"123"
		}
	}
	"UserConfig"
	{
		"BetaKey"		"latest_experimental"
	}
}
`

const sampleAppInfo = `Redirecting stderr to 'C:\steamcmd\logs\stderr.txt'
Loading Steam API...OK
AppID : 294420, change number : 27510000/0, last change : Fri Sep 25 2026
"294420"
{
	"common"
	{
		"name"		"7 Days to Die Dedicated Server"
	}
	"depots"
	{
		"branches"
		{
			"public"
			{
				"buildid"		"19571000"
				"timeupdated"		"1758000000"
			}
			"latest_experimental"
			{
				"buildid"		"19571234"
				"timeupdated"		"1759000000"
				"description"		"Experimental \"test\" build"
			}
			"alpha21.2"
			{
				"buildid"		"12345"
				"pwdrequired"		"1"
			}
		}
	}
}
Unloading Steam API...OK
`

func TestReadInstalledGameFromManifest(t *testing.T) {
	serverDir := t.TempDir()
	if got := readInstalledGame(serverDir); got.Installed {
		t.Fatalf("no manifest = %+v", got)
	}
	if err := os.MkdirAll(filepath.Join(serverDir, "steamapps"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "steamapps", "appmanifest_294420.acf"), []byte(sampleManifest), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readInstalledGame(serverDir); !got.Installed || got.BuildID != "19571234" || got.Branch != "latest_experimental" {
		t.Fatalf("installed = %+v", got)
	}
}

func TestParseAppInfoBranches(t *testing.T) {
	branches, err := parseAppInfoBranches(sampleAppInfo)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 3 || branches[0].Name != "public" || branches[0].BuildID != "19571000" || branches[0].Updated != 1758000000 {
		t.Fatalf("branches = %+v", branches)
	}
	if branches[1].Description != `Experimental "test" build` || !branches[2].Password {
		t.Fatalf("branches = %+v", branches)
	}
	for _, bad := range []string{"no app here", `"294420" { "depots" { `, `"294420" { }`} {
		if _, err := parseAppInfoBranches(bad); err == nil {
			t.Errorf("parseAppInfoBranches(%q) accepted bad input", bad)
		}
	}
}

func TestAppUpdateArgsSelectBranch(t *testing.T) {
	if got := strings.Join(appUpdateArgs("S", SteamSettings{}), " "); got != "+force_install_dir S +login anonymous +app_update 294420 validate +quit" {
		t.Fatalf("default args = %s", got)
	}
	if got := strings.Join(appUpdateArgs("S", SteamSettings{Branch: "alpha21.2", BranchPassword: "pw"}), " "); got != "+force_install_dir S +login anonymous +app_update 294420 -beta alpha21.2 -betapassword pw validate +quit" {
		t.Fatalf("beta args = %s", got)
	}
	for _, bad := range []SteamSettings{{Branch: "-beta"}, {Branch: "a b"}, {Branch: "x", BranchPassword: "has space"}, {BranchPassword: "orphan"}, {Branch: strings.Repeat("a", 65)}} {
		if err := validateSteamSettings(bad); !errors.Is(err, ErrInvalidBranch) {
			t.Errorf("validate(%+v) = %v", bad, err)
		}
	}
	if err := validateSteamSettings(SteamSettings{Branch: "latest_experimental"}); err != nil {
		t.Fatal(err)
	}
}

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"v1.1.0", "v1.0.0", true},
		{"v1.0.10", "v1.0.9", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.0.0-3-gabc1234", false},
		{"v1.0.1", "v1.0.0-3-gabc1234", true},
		{"v0.9.0", "v1.0.0", false},
		{"v2.0.0", "dev", false},
	} {
		if got := newerVersion(c.latest, c.current); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.latest, c.current, got)
		}
	}
}

func TestCheckPanelReadsLatestRelease(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("User-Agent") == "" {
			t.Error("GitHub requires a User-Agent")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","html_url":"https://github.com/example/releases/v1.2.0"}`))
	}))
	defer server.Close()
	checker := NewVersionChecker("v1.0.0")
	checker.releaseURL = server.URL
	if got := checker.CheckPanel(context.Background()); !got.UpdateAvailable || got.Latest != "v1.2.0" || got.URL == "" || got.CheckedAt == nil {
		t.Fatalf("panel = %+v", got)
	}
	status = http.StatusNotFound
	if got := checker.CheckPanel(context.Background()); got.Error == "" || got.UpdateAvailable {
		t.Fatalf("404 = %+v", got)
	}
}

func TestGameBranchesUsesSteamCMDAndReportsUpdates(t *testing.T) {
	u, _ := asyncTestUpdater(t, func(_ context.Context, _ string, args []string, publish func(string)) error {
		if strings.Join(args, " ") != "+login anonymous +app_info_update 1 +app_info_print 294420 +quit" {
			t.Errorf("args = %v", args)
		}
		for _, line := range strings.Split(sampleAppInfo, "\n") {
			publish(line)
		}
		return nil
	})
	if _, err := u.GameBranches(context.Background()); err == nil {
		t.Fatal("a check without SteamCMD must fail")
	}
	if err := os.MkdirAll(u.paths.SteamCMDDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.paths.SteamCMD, nil, 0700); err != nil {
		t.Fatal(err)
	}
	branches, err := u.GameBranches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checker := NewVersionChecker("v1.0.0")
	checker.recordGame(branches, nil)
	installed := InstalledGame{Installed: true, BuildID: "19571000", Branch: "public"}
	if report := checker.Report(installed, SteamSettings{}, true); report.Game.UpdateAvailable || report.Game.Latest != "19571000" {
		t.Fatalf("up to date = %+v", report.Game)
	}
	if report := checker.Report(installed, SteamSettings{Branch: "latest_experimental", BranchPassword: "x"}, true); !report.Game.UpdateAvailable || report.Game.Latest != "19571234" || !report.Steam.HasPassword {
		t.Fatalf("switching branch = %+v", report)
	}
}

func TestVersionSettingsKeepThePasswordPrivate(t *testing.T) {
	a := newTestApp(t)
	rr := serveJSON(t, a, "PUT", "/api/versions/settings", `{"branch":"alpha21.2","password":"secret1","autoCheck":true}`)
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "secret1") {
		t.Fatalf("save = %d %s", rr.Code, rr.Body)
	}
	rr = serveJSON(t, a, "PUT", "/api/versions/settings", `{"branch":"alpha21.2","keepPassword":true,"autoCheck":false}`)
	if rr.Code != 200 {
		t.Fatalf("save = %d %s", rr.Code, rr.Body)
	}
	config, err := LoadPanelConfig(a.paths.PanelConfig)
	if err != nil || config.Steam.BranchPassword != "secret1" || !config.NoUpdateCheck {
		t.Fatalf("config = %+v err=%v", config, err)
	}
	if got := a.steamSettings(); got.Branch != "alpha21.2" {
		t.Fatalf("updater branch = %+v", got)
	}
	if rr := serveJSON(t, a, "PUT", "/api/versions/settings", `{"branch":"bad branch"}`); rr.Code != 400 || apiCode(t, rr) != "invalid_branch" {
		t.Fatalf("bad branch = %d %s", rr.Code, rr.Body)
	}
}

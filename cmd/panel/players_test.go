package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleLP = "Executing command 'lp' by Telnet from 127.0.0.1:51000\r\n" +
	"0. id=171, Steve, the \"Builder\", pos=(-1255.2, 61.1, 355.3), rot=(-5.6, 229.2, 0.0), remote=True, health=113, deaths=2, zombies=40, players=1, score=38, level=12, pltfmid=Steam_76561198000000001, crossid=EOS_0002abcdef, ip=192.168.1.10, ping=12\r\n" +
	"1. id=205, Alex, pos=(10.0, 60.0, -3.5), rot=(0.0, 0.0, 0.0), remote=True, health=90, deaths=0, zombies=3, players=0, score=3, level=2, pltfmid=XBL_2535400000000000, crossid=EOS_0002fedcba, ip=10.0.0.2, ping=48\r\n" +
	"Total of 2 in the game\r\n"

func TestParseOnlinePlayers(t *testing.T) {
	players := parseOnlinePlayers(sampleLP)
	if len(players) != 2 {
		t.Fatalf("players = %+v", players)
	}
	steve := players[0]
	if steve.EntityID != 171 || steve.Name != `Steve, the "Builder"` || steve.PlatformID != "Steam_76561198000000001" || steve.CrossID != "EOS_0002abcdef" || steve.IP != "192.168.1.10" || steve.Ping != 12 || steve.Level != 12 || steve.Zombies != 40 || steve.Deaths != 2 || steve.Position != "-1255.2, 61.1, 355.3" {
		t.Fatalf("steve = %+v", steve)
	}
	if players[1].PlatformID != "XBL_2535400000000000" || players[1].Ping != 48 {
		t.Fatalf("alex = %+v", players[1])
	}
	if got := parseOnlinePlayers("Total of 0 in the game\r\n"); len(got) != 0 {
		t.Fatalf("empty = %+v", got)
	}
}

func TestReadAdminListsAcrossVersions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serveradmin.xml")
	if got := readAdminLists(path); got.Found || len(got.Admins) != 0 {
		t.Fatalf("missing file = %+v", got)
	}
	content := `<?xml version="1.0" encoding="UTF-8"?>
<adminTools>
  <users>
    <!-- <user platform="Steam" userid="1" name="commented" permission_level="0" /> -->
    <user platform="Steam" userid="76561198000000001" name="Steve" permission_level="0" />
  </users>
  <admins>
    <admin steamID="76561198000000009" permission_level="1" />
  </admins>
  <groups><group steamID="123" name="x" permission_level_default="1000" /></groups>
  <whitelist>
    <user platform="XBL" userid="2535400000000000" name="Alex" />
  </whitelist>
  <blacklist>
    <blacklisted platform="Steam" userid="76561198000000002" name="Griefer" unbandate="2099-01-01 00:00:00" reason="griefing" />
  </blacklist>
  <commands><permission cmd="dm" permission_level="0" /></commands>
</adminTools>`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	lists := readAdminLists(path)
	if !lists.Found || lists.Error != "" || len(lists.Admins) != 2 || lists.Admins[0].ID != "Steam_76561198000000001" || *lists.Admins[0].Permission != 0 || lists.Admins[1].ID != "Steam_76561198000000009" {
		t.Fatalf("admins = %+v", lists)
	}
	if len(lists.Whitelist) != 1 || lists.Whitelist[0].ID != "XBL_2535400000000000" {
		t.Fatalf("whitelist = %+v", lists.Whitelist)
	}
	if len(lists.Banned) != 1 || lists.Banned[0].Reason != "griefing" || lists.Banned[0].Until == "" {
		t.Fatalf("banned = %+v", lists.Banned)
	}
}

func TestAdminFilePathFollowsServerConfig(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	if got := adminFilePath(paths); got != filepath.Join(paths.Saves, "serveradmin.xml") {
		t.Fatalf("default = %s", got)
	}
	custom := filepath.Join(paths.Root, "custom-saves")
	writeServerConfig(t, paths, `<ServerSettings><property name="AdminFileName" value="admins.xml"/><property name="SaveGameFolder" value="`+custom+`"/></ServerSettings>`)
	if got := adminFilePath(paths); got != filepath.Join(custom, "admins.xml") {
		t.Fatalf("custom = %s", got)
	}
	writeServerConfig(t, paths, `<ServerSettings><property name="AdminFileName" value="..\evil.xml"/></ServerSettings>`)
	if got := adminFilePath(paths); filepath.Base(got) != "serveradmin.xml" {
		t.Fatalf("a path in AdminFileName must be ignored: %s", got)
	}
}

func writeServerConfig(t *testing.T, paths Paths, content string) {
	t.Helper()
	if err := os.MkdirAll(paths.ServerDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ServerConfig, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPlayerCommands(t *testing.T) {
	for _, c := range []struct {
		action PlayerAction
		want   string
	}{
		{PlayerAction{Action: "kick", Target: "171", Reason: `be "nice"`}, `kick 171 "be 'nice'"`},
		{PlayerAction{Action: "ban", Target: "Steam_765", Duration: 7, Unit: "days", Reason: "griefing"}, `ban add Steam_765 7 days "griefing"`},
		{PlayerAction{Action: "unban", Target: "Steam_765"}, `ban remove Steam_765`},
		{PlayerAction{Action: "whitelist_add", Target: "EOS_00a"}, `whitelist add EOS_00a`},
		{PlayerAction{Action: "whitelist_remove", Target: "EOS_00a"}, `whitelist remove EOS_00a`},
		{PlayerAction{Action: "admin_add", Target: "Steam_765", Level: 0}, `admin add Steam_765 0`},
		{PlayerAction{Action: "admin_remove", Target: "Steam_765"}, `admin remove Steam_765`},
		{PlayerAction{Action: "pm", Target: "171", Message: "hi"}, `sayplayer 171 "hi"`},
		{PlayerAction{Action: "say", Message: "hello all"}, `say "hello all"`},
	} {
		got, err := playerCommand(c.action)
		if err != nil || got != c.want {
			t.Errorf("%+v = %q, %v; want %q", c.action, got, err, c.want)
		}
	}
	for _, bad := range []PlayerAction{
		{Action: "kick", Target: "171; shutdown"},
		{Action: "kick", Target: "a b"},
		{Action: "kick", Target: ""},
		{Action: "ban", Target: "1", Duration: 0, Unit: "days"},
		{Action: "ban", Target: "1", Duration: 1, Unit: "centuries"},
		{Action: "admin_add", Target: "1", Level: 1001},
		{Action: "say", Message: ""},
		{Action: "say", Message: "a\nshutdown"},
		{Action: "shutdown", Target: "1"},
	} {
		if _, err := playerCommand(bad); !errors.Is(err, ErrInvalidPlayerAction) {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}
}

func TestPlayerHistoryRemembersPlayers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "players.json")
	history := NewPlayerHistory(path)
	start := time.Unix(1000, 0)
	if err := history.Record(parseOnlinePlayers(sampleLP), start); err != nil {
		t.Fatal(err)
	}
	later := start.Add(time.Hour)
	if err := history.Record([]OnlinePlayer{{Name: "Alex Renamed", PlatformID: "XBL_2535400000000000"}}, later); err != nil {
		t.Fatal(err)
	}
	reloaded := NewPlayerHistory(path).List()
	if len(reloaded) != 2 || reloaded[0].Name != "Alex Renamed" || !reloaded[0].FirstSeen.Equal(start) || !reloaded[0].LastSeen.Equal(later) || reloaded[0].IP != "10.0.0.2" {
		t.Fatalf("history = %+v", reloaded)
	}
}

func TestPlayersRouteAndActions(t *testing.T) {
	a := newTestApp(t)
	if rr := serveJSON(t, a, "GET", "/api/players", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"running":false`) {
		t.Fatalf("stopped players = %d %s", rr.Code, rr.Body)
	}
	if rr := serveJSON(t, a, "POST", "/api/players/action", `{"action":"kick","target":"171"}`); rr.Code != 409 {
		t.Fatalf("action while stopped = %d %s", rr.Code, rr.Body)
	}
	a.server.mu.Lock()
	a.server.state = ServerRunning
	a.server.launched = &fakeLaunchedProcess{pid: 42}
	a.server.mu.Unlock()
	commands := scriptTelnet(t, a, func(command string) string {
		if command == "lp" {
			return sampleLP
		}
		return "ok"
	})
	rr := serveJSON(t, a, "GET", "/api/players", "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"entityId":171`) || !strings.Contains(rr.Body.String(), `"known":[{`) {
		t.Fatalf("running players = %d %s", rr.Code, rr.Body)
	}
	if rr := serveJSON(t, a, "POST", "/api/players/action", `{"action":"ban","target":"Steam_76561198000000001","duration":1,"unit":"hours","reason":"x"}`); rr.Code != 200 {
		t.Fatalf("ban = %d %s", rr.Code, rr.Body)
	}
	if rr := serveJSON(t, a, "POST", "/api/players/action", `{"action":"kick","target":"1 && shutdown"}`); rr.Code != 400 || apiCode(t, rr) != "invalid_player_action" {
		t.Fatalf("bad target = %d %s", rr.Code, rr.Body)
	}
	if got := strings.Join(commands(), "|"); got != `lp|ban add Steam_76561198000000001 1 hours "x"` {
		t.Fatalf("commands = %s", got)
	}
}

func TestNaiwaziDetection(t *testing.T) {
	paths := ResolvePaths(t.TempDir())
	writeServerConfig(t, paths, `<ServerSettings><property name="ServerPort" value="27000"/></ServerSettings>`)
	if got := naiwaziStatus(paths, 0, nil); got.Installed || got.Port != 27006 || !got.PortFromGame {
		t.Fatalf("not installed = %+v", got)
	}
	folder := filepath.Join(paths.Mods, "NaiwaziBot")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	modInfo := `<?xml version="1.0" encoding="UTF-8" ?><xml><Name value="NaiwaziBot" /><DisplayName value="NaiwaziBot" /><Version value="4.4.1" /><Author value="NAIWAZI" /></xml>`
	if err := os.WriteFile(filepath.Join(folder, "ModInfo.xml"), []byte(modInfo), 0600); err != nil {
		t.Fatal(err)
	}
	dialed := ""
	got := naiwaziStatus(paths, 0, func(address string) bool { dialed = address; return true })
	if !got.Installed || !got.Enabled || got.Version != "4.4.1" || !got.Listening || got.URL != "http://127.0.0.1:27006/" || dialed != "127.0.0.1:27006" {
		t.Fatalf("installed = %+v", got)
	}
	if got := naiwaziStatus(paths, 30000, nil); got.Port != 30000 || got.PortFromGame {
		t.Fatalf("override = %+v", got)
	}
	if _, _, err := readNaiwaziPassword(paths); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no password file = %v", err)
	}
	passwordFile := filepath.Join(paths.ServerDir, "NaiwaziBot_Data", "NaiwaziBot_Password.txt")
	if err := os.MkdirAll(filepath.Dir(passwordFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordFile, []byte("\ufeff用户名: admin\r\n密码: 123456\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	text, file, err := readNaiwaziPassword(paths)
	if err != nil || file != passwordFile || !strings.HasPrefix(text, "用户名") {
		t.Fatalf("password = %q %s %v", text, file, err)
	}
	if got := naiwaziStatus(paths, 0, nil); got.PasswordFile != passwordFile {
		t.Fatalf("password file = %+v", got)
	}
}

func TestPollObserverRecordsPlayers(t *testing.T) {
	a := newTestApp(t)
	a.server.mu.Lock()
	a.server.state = ServerRunning
	a.server.launched = &fakeLaunchedProcess{pid: 42}
	a.server.poll = func(context.Context) (string, error) { return sampleLP, nil }
	a.server.mu.Unlock()
	a.server.pollOnce(context.Background())
	if known := a.players.List(); len(known) != 2 {
		t.Fatalf("known = %+v", known)
	}
}

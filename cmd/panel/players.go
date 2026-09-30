package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Player management goes through the game's own console commands over
// Telnet (lp, kick, ban, whitelist, admin, say, sayplayer); the lists of
// admins, whitelisted and banned players are read from serveradmin.xml,
// which the game keeps up to date.

type OnlinePlayer struct {
	EntityID   int    `json:"entityId"`
	Name       string `json:"name"`
	PlatformID string `json:"platformId,omitempty"`
	CrossID    string `json:"crossId,omitempty"`
	IP         string `json:"ip,omitempty"`
	Ping       int    `json:"ping"`
	Level      int    `json:"level"`
	Health     int    `json:"health"`
	Deaths     int    `json:"deaths"`
	Zombies    int    `json:"zombies"`
	Players    int    `json:"players"`
	Score      int    `json:"score"`
	Position   string `json:"position,omitempty"`
}

var lpLine = regexp.MustCompile(`^\s*\d+\.\s+id=(\d+),\s+(.*?),\s+pos=\(([^)]*)\),\s*(.*)$`)

// parseOnlinePlayers reads the output of "lp" (listplayers). Player names
// may contain commas, so the name is everything between the id and pos.
func parseOnlinePlayers(output string) []OnlinePlayer {
	players := []OnlinePlayer{}
	for _, line := range strings.Split(output, "\n") {
		match := lpLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if match == nil {
			continue
		}
		id, _ := strconv.Atoi(match[1])
		player := OnlinePlayer{EntityID: id, Name: match[2], Position: strings.TrimSpace(match[3])}
		for key, value := range lpFields(match[4]) {
			number, _ := strconv.Atoi(value)
			switch key {
			case "pltfmid":
				player.PlatformID = value
			case "crossid":
				player.CrossID = value
			case "ip":
				player.IP = value
			case "ping":
				player.Ping = number
			case "level":
				player.Level = number
			case "health":
				player.Health = number
			case "deaths":
				player.Deaths = number
			case "zombies":
				player.Zombies = number
			case "players":
				player.Players = number
			case "score":
				player.Score = number
			case "steamid": // before V1.0
				if player.PlatformID == "" {
					player.PlatformID = "Steam_" + value
				}
			}
		}
		players = append(players, player)
	}
	return players
}

// lpFields splits "a=1, b=(x, y), c=3" at top-level commas.
func lpFields(text string) map[string]string {
	fields := map[string]string{}
	depth, start := 0, 0
	flush := func(end int) {
		part := strings.TrimSpace(text[start:end])
		if key, value, ok := strings.Cut(part, "="); ok {
			fields[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	for i, r := range text {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				flush(i)
				start = i + 1
			}
		}
	}
	flush(len(text))
	return fields
}

// AdminEntry is one user in serveradmin.xml.
type AdminEntry struct {
	ID         string `json:"id"` // Platform_UserID, as the console commands take it
	Name       string `json:"name,omitempty"`
	Permission *int   `json:"permission,omitempty"`
	Until      string `json:"until,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type AdminLists struct {
	File      string       `json:"file"`
	Found     bool         `json:"found"`
	Error     string       `json:"error,omitempty"`
	Admins    []AdminEntry `json:"admins"`
	Whitelist []AdminEntry `json:"whitelist"`
	Banned    []AdminEntry `json:"banned"`
}

// adminFilePath finds serveradmin.xml: AdminFileName in SaveGameFolder,
// which defaults to <UserDataFolder>\Saves.
func adminFilePath(paths Paths) string {
	name, folder := "serveradmin.xml", paths.Saves
	if doc, err := LoadConfig(paths.ServerConfig); err == nil {
		if value, ok := configValue(doc, "AdminFileName"); ok && value != "" && value != ".." && !strings.ContainsAny(value, `/\:`) {
			name = value
		}
		if value, ok := configValue(doc, "SaveGameFolder"); ok && strings.TrimSpace(value) != "" && filepath.IsAbs(value) {
			folder = value
		} else if value, ok := configValue(doc, "UserDataFolder"); ok && strings.TrimSpace(value) != "" && filepath.IsAbs(value) {
			folder = filepath.Join(value, "Saves")
		}
	}
	return filepath.Join(folder, name)
}

// readAdminLists tolerates the layouts of different game versions: users
// are in <users> (V1+) or <admins> (A20), identified by platform+userid or
// by steamID.
func readAdminLists(path string) AdminLists {
	lists := AdminLists{File: path, Admins: []AdminEntry{}, Whitelist: []AdminEntry{}, Banned: []AdminEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return lists
	}
	if err != nil {
		lists.Error = sanitizedError(err)
		return lists
	}
	lists.Found = true
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	var section []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			lists.Error = sanitizedError(err)
			break
		}
		switch element := token.(type) {
		case xml.StartElement:
			name := strings.ToLower(element.Name.Local)
			if len(section) == 2 {
				entry, ok := adminEntry(element.Attr)
				if ok {
					switch section[1] {
					case "users", "admins":
						if entry.Permission != nil {
							lists.Admins = append(lists.Admins, entry)
						}
					case "whitelist":
						lists.Whitelist = append(lists.Whitelist, entry)
					case "blacklist":
						lists.Banned = append(lists.Banned, entry)
					}
				}
			}
			section = append(section, name)
		case xml.EndElement:
			if len(section) > 0 {
				section = section[:len(section)-1]
			}
		}
	}
	return lists
}

func adminEntry(attrs []xml.Attr) (AdminEntry, bool) {
	values := map[string]string{}
	for _, attr := range attrs {
		values[strings.ToLower(attr.Name.Local)] = attr.Value
	}
	entry := AdminEntry{Name: values["name"], Until: values["unbandate"], Reason: values["reason"]}
	switch {
	case values["userid"] != "" && values["platform"] != "":
		entry.ID = values["platform"] + "_" + values["userid"]
	case values["userid"] != "":
		entry.ID = values["userid"]
	case values["steamid"] != "":
		entry.ID = "Steam_" + values["steamid"]
	default:
		return entry, false
	}
	if level, err := strconv.Atoi(values["permission_level"]); err == nil {
		entry.Permission = &level
	}
	return entry, true
}

// ---------- Commands ----------

type PlayerAction struct {
	Action   string `json:"action"`
	Target   string `json:"target,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Duration int    `json:"duration,omitempty"`
	Unit     string `json:"unit,omitempty"`
	Level    int    `json:"level,omitempty"`
	Message  string `json:"message,omitempty"`
}

var (
	ErrInvalidPlayerAction = errors.New("invalid player action")
	playerTarget           = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	banUnits               = []string{"minutes", "hours", "days", "weeks", "months", "years"}
)

// quoteArgument makes free text safe as one quoted console argument.
func quoteArgument(text string, limit int) (string, error) {
	text = strings.TrimSpace(text)
	if len([]rune(text)) > limit {
		return "", fmt.Errorf("%w: text is longer than %d characters", ErrInvalidPlayerAction, limit)
	}
	if strings.ContainsFunc(text, unicode.IsControl) {
		return "", fmt.Errorf("%w: text must be one line", ErrInvalidPlayerAction)
	}
	return `"` + strings.ReplaceAll(text, `"`, "'") + `"`, nil
}

// playerCommand turns an action into one console command.
func playerCommand(action PlayerAction) (string, error) {
	needsTarget := action.Action != "say"
	if needsTarget && !playerTarget.MatchString(action.Target) {
		return "", fmt.Errorf("%w: target must be an entity id, platform id or player name without spaces", ErrInvalidPlayerAction)
	}
	switch action.Action {
	case "kick":
		reason, err := quoteArgument(action.Reason, 200)
		if err != nil {
			return "", err
		}
		return "kick " + action.Target + " " + reason, nil
	case "ban":
		if action.Duration < 1 || action.Duration > 100000 || !contains(banUnits, action.Unit) {
			return "", fmt.Errorf("%w: ban needs a duration and a unit", ErrInvalidPlayerAction)
		}
		reason, err := quoteArgument(action.Reason, 200)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ban add %s %d %s %s", action.Target, action.Duration, action.Unit, reason), nil
	case "unban":
		return "ban remove " + action.Target, nil
	case "whitelist_add":
		return "whitelist add " + action.Target, nil
	case "whitelist_remove":
		return "whitelist remove " + action.Target, nil
	case "admin_add":
		if action.Level < 0 || action.Level > 1000 {
			return "", fmt.Errorf("%w: permission level must be 0-1000", ErrInvalidPlayerAction)
		}
		return fmt.Sprintf("admin add %s %d", action.Target, action.Level), nil
	case "admin_remove":
		return "admin remove " + action.Target, nil
	case "pm", "say":
		message, err := quoteArgument(action.Message, 300)
		if err != nil || message == `""` {
			return "", fmt.Errorf("%w: a message is required", ErrInvalidPlayerAction)
		}
		if action.Action == "say" {
			return "say " + message, nil
		}
		return "sayplayer " + action.Target + " " + message, nil
	}
	return "", fmt.Errorf("%w: unknown action %q", ErrInvalidPlayerAction, action.Action)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// ---------- Players seen by the panel ----------

// KnownPlayer is remembered from the online list so that offline players
// can still be banned, whitelisted or made admin.
type KnownPlayer struct {
	PlatformID string    `json:"platformId"`
	CrossID    string    `json:"crossId,omitempty"`
	Name       string    `json:"name"`
	IP         string    `json:"ip,omitempty"`
	FirstSeen  time.Time `json:"firstSeen"`
	LastSeen   time.Time `json:"lastSeen"`
}

const maxKnownPlayers = 2000

type PlayerHistory struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	players map[string]*KnownPlayer
	saved   time.Time
}

func NewPlayerHistory(path string) *PlayerHistory {
	return &PlayerHistory{path: path, players: map[string]*KnownPlayer{}}
}

func (h *PlayerHistory) loadLocked() {
	if h.loaded {
		return
	}
	h.loaded = true
	data, err := os.ReadFile(h.path)
	if err != nil {
		return
	}
	var list []KnownPlayer
	if json.Unmarshal(data, &list) != nil {
		return
	}
	for i := range list {
		if list[i].PlatformID != "" {
			player := list[i]
			h.players[player.PlatformID] = &player
		}
	}
}

// Record notes the online players; it writes the file when someone new
// appears or at most every five minutes otherwise.
func (h *PlayerHistory) Record(online []OnlinePlayer, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadLocked()
	changed := false
	for _, player := range online {
		if player.PlatformID == "" {
			continue
		}
		known := h.players[player.PlatformID]
		if known == nil {
			known = &KnownPlayer{PlatformID: player.PlatformID, FirstSeen: now}
			h.players[player.PlatformID] = known
			changed = true
		}
		if known.Name != player.Name || (player.IP != "" && known.IP != player.IP) {
			changed = true
		}
		known.Name, known.CrossID, known.LastSeen = player.Name, player.CrossID, now
		if player.IP != "" {
			known.IP = player.IP
		}
	}
	if !changed && now.Sub(h.saved) < 5*time.Minute {
		return nil
	}
	list := h.listLocked()
	if len(list) > maxKnownPlayers {
		for _, old := range list[maxKnownPlayers:] {
			delete(h.players, old.PlatformID)
		}
		list = list[:maxKnownPlayers]
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0700); err != nil {
		return err
	}
	temp := h.path + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	if err := replaceFile(temp, h.path); err != nil {
		return err
	}
	h.saved = now
	return nil
}

// List returns the known players, most recently seen first.
func (h *PlayerHistory) List() []KnownPlayer {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loadLocked()
	return h.listLocked()
}

func (h *PlayerHistory) listLocked() []KnownPlayer {
	list := make([]KnownPlayer, 0, len(h.players))
	for _, player := range h.players {
		list = append(list, *player)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].LastSeen.Equal(list[j].LastSeen) {
			return list[i].LastSeen.After(list[j].LastSeen)
		}
		return list[i].PlatformID < list[j].PlatformID
	})
	return list
}

package main

import (
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const consoleCap = 5000

var (
	consoleWindowsPath      = regexp.MustCompile(`(?i)(?:["'](?:[a-z]:[\\/]|\\\\)[^"'\r\n]+["']|(?:[a-z]:[\\/]|\\\\)(?:[^\\\s,;]+(?:\s+[^\\\s,;]+)*[\\/])*[^\\\s,;]+(?:\s+[^\\\s,;]+)*\.[^\\\s,;]+)`)
	consoleWindowsDirectory = regexp.MustCompile(`(?i)((?:[a-z]:[\\/]|\\\\)(?:[^\\\s,;]+(?:\s+[^\\\s,;]+)*[\\/])*[^\\\s,;]+(?:\s+[^\\\s,;]+)*)([,;]|$)`)
	consoleUnixPath         = regexp.MustCompile(`(^|[\s(])/(?:[^\s,;]+)`)
	consoleSecret           = regexp.MustCompile(`(?i)(?:password|token|secret|authorization|api[_-]?key)`)
	consoleHeaderValue      = regexp.MustCompile(`(?i)(^|[^[:alnum:]_-])(["']?(?:x-sdtd-api-(?:secret|tokenname)|authorization|set-cookie|cookie)["']?\s*(?::|=|\s)\s*)[^\r\n]*`)
	consoleKeyValue         = regexp.MustCompile(`(?i)(^|[^[:alnum:]_-])(["']?(?:password|token|secret|api[_-]?key)["']?\s*(?::|=|\s)\s*)(?:"[^"]*"|'[^']*'|(?:bearer\s+)?[^\s,;}\]]+)`)
)

type ConsoleEntry struct {
	Cursor  uint64    `json:"cursor"`
	Channel string    `json:"channel"`
	Level   string    `json:"level"`
	Kind    string    `json:"kind"`
	Text    string    `json:"text"`
	Time    time.Time `json:"time"`
}

type ConsoleStore struct {
	mu      sync.Mutex
	entries []ConsoleEntry
	cursor  uint64
}

func NewConsoleStore() *ConsoleStore { return &ConsoleStore{} }

func consoleChannel(channel string) string {
	switch channel {
	case "panel", "steamcmd", "game", "telnet", "ops":
		return channel
	default:
		return "panel"
	}
}

func consoleLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "info", "warn", "error", "command", "output":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

func sanitizeConsoleText(text string) string {
	text = consoleHeaderValue.ReplaceAllString(text, `${1}${2}[REDACTED]`)
	text = consoleKeyValue.ReplaceAllString(text, `${1}${2}[REDACTED]`)
	text = consoleWindowsPath.ReplaceAllString(text, "[REDACTED]")
	text = consoleWindowsDirectory.ReplaceAllString(text, "[REDACTED]${2}")
	text = sanitizeAuditText(text)
	return consoleUnixPath.ReplaceAllString(text, `${1}[REDACTED]`)
}

func (s *ConsoleStore) Append(channel, level, kind, text string) ConsoleEntry {
	entry := ConsoleEntry{Channel: consoleChannel(channel), Level: consoleLevel(level), Kind: strings.TrimSpace(kind), Text: sanitizeConsoleText(text), Time: time.Now().UTC()}
	s.mu.Lock()
	s.cursor++
	entry.Cursor = s.cursor
	if len(s.entries) == consoleCap {
		copy(s.entries, s.entries[1:])
		s.entries = s.entries[:consoleCap-1]
	}
	s.entries = append(s.entries, entry)
	s.mu.Unlock()
	return entry
}

func (s *ConsoleStore) Since(channel string, after uint64, limit int) []ConsoleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []ConsoleEntry
	for _, entry := range s.entries {
		if entry.Channel == channel && (after == 0 || entry.Cursor > after) {
			result = append(result, entry)
		}
	}
	if after == 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}
	if after != 0 && len(result) > limit {
		result = result[:limit]
	}
	return append([]ConsoleEntry(nil), result...)
}

func validConsoleCommand(command string) bool {
	if command = strings.TrimSpace(command); command == "" || len([]rune(command)) > 500 || consoleSecret.MatchString(command) {
		return false
	}
	for _, r := range command {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

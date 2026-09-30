package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestConsoleStoreBoundsCursorsAndSince(t *testing.T) {
	store := NewConsoleStore()
	for i := range 5001 {
		store.Append("game", "verbose", "log", fmt.Sprint(i))
	}
	latest := store.Since("game", 0, 2)
	if len(latest) != 2 || latest[0].Cursor != 5000 || latest[1].Cursor != 5001 || latest[0].Level != "info" {
		t.Fatalf("latest = %#v", latest)
	}
	after := store.Since("game", 5000, 500)
	if len(after) != 1 || after[0].Cursor != 5001 {
		t.Fatalf("after = %#v", after)
	}
}

func TestConsoleStoreSanitizesBeforeStorage(t *testing.T) {
	store := NewConsoleStore()
	entry := store.Append("telnet", "error", "output", `password hunter2 X-Panel-Token: abc C:\Users\private\token.txt`)
	if entry.Channel != "telnet" || entry.Level != "error" || strings.Contains(entry.Text, "hunter2") || strings.Contains(entry.Text, "abc") || strings.Contains(entry.Text, `C:\Users\private`) {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestConsoleStoreRedactsSensitiveHeaderValues(t *testing.T) {
	store := NewConsoleStore()
	for _, test := range []struct {
		text, secret string
	}{
		{"X-SDTD-API-SECRET: secret-sentinel", "secret-sentinel"},
		{"X-SDTD-API-TOKENNAME = token-name-sentinel", "token-name-sentinel"},
		{"Authorization: Bearer bearer-sentinel", "bearer-sentinel"},
		{"Cookie: session=cookie-sentinel", "cookie-sentinel"},
		{"Set-Cookie: session=set-cookie-sentinel; Path=/", "set-cookie-sentinel"},
		{"password password-sentinel", "password-sentinel"},
		{"token = token-sentinel", "token-sentinel"},
		{"secret: secret-sentinel", "secret-sentinel"},
		{"api-key=api-key-sentinel", "api-key-sentinel"},
	} {
		store.Append("panel", "info", "log", test.text)
		stored := store.Since("panel", 0, 1)[0]
		if strings.Contains(stored.Text, test.secret) {
			t.Fatalf("stored %q leaked %q", stored.Text, test.secret)
		}
	}
	if stored := store.Append("panel", "info", "log", "server started normally"); stored.Text != "server started normally" {
		t.Fatalf("ordinary log changed to %q", stored.Text)
	}
}

func TestBrokerRedactsStoredAndStreamedMessages(t *testing.T) {
	broker, store := NewEventBroker(), NewConsoleStore()
	broker.SetConsoleStore(store)
	broker.Publish(Event{Type: "log", Source: "server", Message: `opened "C:\Program Files\7DTD\serverconfig.xml" token=secret-sentinel`})
	if got := store.Since("game", 0, 1)[0].Text; strings.Contains(got, "Files") || strings.Contains(got, "secret-sentinel") {
		t.Fatalf("console leaked %q", got)
	}
	_, cancel, events := broker.subscribe()
	defer cancel()
	if len(events) != 1 || strings.Contains(events[0].Message, "Files") || strings.Contains(events[0].Message, "secret-sentinel") {
		t.Fatalf("stream leaked %#v", events)
	}
}

func TestConsoleRedactsUnquotedWindowsPathsWithSpaces(t *testing.T) {
	for _, test := range []struct{ text, want string }{
		{`C:\Program Files\Secret\token.txt`, "[REDACTED]"},
		{`C:\Program Files`, "[REDACTED]"},
		{`C:\Program Files\Secret\token.txt, next`, "[REDACTED], next"},
		{`C:\Program Files\Secret\token.txt; next`, "[REDACTED]; next"},
		{`"C:\Program Files\Secret\token.txt"`, "[REDACTED]"},
		{`C:\server\token.txt`, "[REDACTED]"},
		{`open C:\Program Files\Secret\token.txt failed to load`, "open [REDACTED] failed to load"},
	} {
		text := test.text
		got := sanitizeConsoleText(text)
		if strings.Contains(got, `Files`) || strings.Contains(got, `Secret\token.txt`) || strings.Contains(got, `C:\`) {
			t.Fatalf("path leaked from %q as %q", text, got)
		}
		if got != test.want {
			t.Fatalf("sanitizeConsoleText(%q)=%q, want %q", text, got, test.want)
		}
	}
	if got := sanitizeConsoleText("server started normally"); got != "server started normally" {
		t.Fatalf("ordinary prose=%q", got)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditLogWritesOneSanitizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "panel.log")
	log := NewAuditLog(path)
	if err := log.Write("config save\npassword=secret-sentinel"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Count(text, "\n") != 1 || strings.Contains(text, "secret-sentinel") || !strings.Contains(text, "config save") {
		t.Fatalf("audit line = %q", text)
	}
}

func TestAuditSanitizerRedactsCredentialSyntaxes(t *testing.T) {
	for _, input := range []string{
		"password hunter2",
		"token: abc",
		"TOKEN=def",
		`{"token":"json-sentinel"}`,
		"Authorization: Bearer bearer-sentinel",
	} {
		got := sanitizeAuditText(input)
		for _, secret := range []string{"hunter2", "abc", "def", "json-sentinel", "bearer-sentinel"} {
			if strings.Contains(got, secret) {
				t.Fatalf("sanitize %q leaked %q in %q", input, secret, got)
			}
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("sanitize %q = %q", input, got)
		}
	}
}

func TestUpdaterFailurePersistsSanitizedReasonToAuditStatusAndEvents(t *testing.T) {
	u, events := asyncTestUpdater(t, func(context.Context, string, []string, func(string)) error {
		t.Fatal("runner reached after pre-SteamCMD failure")
		return nil
	})
	u.paths.ServerConfig = filepath.Join(u.paths.Root, "secret-sentinel", "missing.xml")
	auditPath := filepath.Join(u.paths.Logs, "panel.log")
	u.audit = NewAuditLog(auditPath).Write
	if err := u.Start(context.Background(), UpdateServer); err != nil {
		t.Fatal(err)
	}
	status := waitUpdateState(t, u, "error")
	if err := u.Wait(); err == nil {
		t.Fatal("pre-SteamCMD failure was not returned")
	}
	if !strings.Contains(status.LastError, "before SteamCMD") || strings.Contains(status.LastError, "secret-sentinel") {
		t.Fatalf("status = %#v", status)
	}
	b, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(b); !strings.Contains(text, "before SteamCMD") || strings.Contains(text, "secret-sentinel") {
		t.Fatalf("audit = %q", text)
	}
	_, cancel, history := events.subscribe()
	defer cancel()
	last := history[len(history)-1].Message
	if !strings.Contains(last, "before SteamCMD") || strings.Contains(last, "secret-sentinel") {
		t.Fatalf("event = %q", last)
	}
}

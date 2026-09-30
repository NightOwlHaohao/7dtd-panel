package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	ErrAuditUnavailable = errors.New("audit log unavailable")
	auditCredential     = regexp.MustCompile(`(?i)(["']?(?:password|token|session[_-]?token|authorization)["']?\s*(?:=|:)?\s*)(?:"[^"]*"|'[^']*'|(?:bearer\s+)?[^\s,;}\]]+)`)
	auditBearer         = regexp.MustCompile(`(?i)(\bbearer\s+)[^\s,;}\]]+`)
)

type AuditLog struct {
	path string
	mu   sync.Mutex
}

func NewAuditLog(path string) *AuditLog { return &AuditLog{path: path} }

func (l *AuditLog) Write(message string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + " " + sanitizeAuditText(message) + "\n")
	return errors.Join(writeErr, f.Close())
}

func sanitizeAuditText(text string) string {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	text = auditCredential.ReplaceAllString(text, `${1}[REDACTED]`)
	text = auditBearer.ReplaceAllString(text, `${1}[REDACTED]`)
	fields := strings.Fields(text)
	for i, field := range fields {
		if strings.Contains(strings.ToLower(field), "secret") {
			fields[i] = "[REDACTED]"
		}
	}
	runes := []rune(strings.Join(fields, " "))
	if len(runes) > 1000 {
		runes = append(runes[:997], '.', '.', '.')
	}
	return string(runes)
}

func sanitizedError(err error) string {
	if err == nil {
		return ""
	}
	if text := sanitizeAuditText(err.Error()); text != "" {
		return text
	}
	return "unknown error"
}

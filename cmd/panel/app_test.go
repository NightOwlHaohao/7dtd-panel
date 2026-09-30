package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppGETRoutesExist(t *testing.T) {
	a := newTestApp(t)
	for _, path := range []string{
		"/api/session", "/api/setup", "/api/status", "/api/config",
		"/api/sandbox", "/api/events", "/api/backups", "/api/update", "/api/performance",
		"/api/players", "/api/schedule", "/api/versions", "/api/panel",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787"+path, nil)
			if path == "/api/events" {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			req.RemoteAddr = "127.0.0.1:51000"
			rr := httptest.NewRecorder()
			a.Handler().ServeHTTP(rr, req)
			if rr.Code == http.StatusNotFound {
				t.Fatalf("%s returned 404", path)
			}
		})
	}
}

func TestEmbeddedShellWiresEveryPage(t *testing.T) {
	page := string(readEmbedded(t, "index.html"))
	for _, want := range []string{`id="main"`, `id="nav"`, `id="console-drawer"`, `id="confirm-dialog"`, `<script type="module" src="/js/main.js">`} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html missing %s", want)
		}
	}
	router := string(readEmbedded(t, "js/main.js"))
	for _, id := range []string{"overview", "server", "players", "schedule", "config", "sandbox", "mods", "saves", "backups", "firewall", "panel"} {
		readEmbedded(t, "js/pages/"+id+".js")
		if !strings.Contains(router, `id: "`+id+`"`) {
			t.Errorf("router does not register page %q", id)
		}
	}
}

func TestEmbeddedThemesAndLanguages(t *testing.T) {
	tokens := string(readEmbedded(t, "css/tokens.css"))
	for _, want := range []string{`:root[data-theme="dark"]`, `:not([data-theme="light"])`, "prefers-color-scheme: dark"} {
		if !strings.Contains(tokens, want) {
			t.Errorf("tokens.css missing %q", want)
		}
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en"} {
		readEmbedded(t, "js/locales/"+locale+".js")
	}
}

func TestEmbeddedAssetsExcludeTests(t *testing.T) {
	for _, name := range []string{"tests", "package.json", "embed.go"} {
		if _, err := web.ReadFile(name); err == nil {
			t.Errorf("%s must not be embedded", name)
		}
	}
}

func readEmbedded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := web.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

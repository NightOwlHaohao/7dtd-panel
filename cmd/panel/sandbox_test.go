package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const friendCode = "AAAMABKACKADKAEIAFJAGIAHJAKEAMLANBAOBARMAZABACBBABGBBHABMDBNEBZECAACBACHACOHDLAEIJEPAERKETKEUAEXGFACFDBFFKFKAGHL"

func TestSandboxRoundTripKeepsUnknown163(t *testing.T) {
	c, err := ParseSandboxCode(friendCode)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Records) != 37 {
		t.Fatalf("records=%d", len(c.Records))
	}
	last := c.Records[len(c.Records)-1]
	if last.OptionID != 163 || last.ValueIndex != 11 {
		t.Fatalf("last=%+v", last)
	}
	if c.String() != friendCode {
		t.Fatalf("round trip changed code")
	}
}

func TestMergeKnownEditPreservesUnknown(t *testing.T) {
	c, _ := ParseSandboxCode(friendCode)
	got := MergeSandbox(c, map[int]int{0: 9})
	parsed, _ := ParseSandboxCode(got.String())
	if parsed.ValueOf(163) != 11 {
		t.Fatal("unknown option 163 was lost")
	}
}

func TestParseSandboxCodeRejectsInvalidAndWarnsDuplicates(t *testing.T) {
	if _, err := ParseSandboxCode("AA"); err == nil {
		t.Fatal("accepted invalid length")
	}
	if _, err := ParseSandboxCode("AAaA"); err == nil {
		t.Fatal("accepted lower case")
	}
	if _, err := ParseSandboxCode("AAAAAAA"); !errors.Is(err, ErrSandboxDuplicateOption) {
		t.Fatalf("got %v", err)
	}
}

func TestSandboxParseErrorReportsOffsetAndRecord(t *testing.T) {
	_, err := ParseSandboxCode("AAaA")
	var parseErr *SandboxParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("err=%T %v", err, err)
	}
	if parseErr.Offset != 2 || parseErr.Record != 0 {
		t.Fatalf("error=%#v", parseErr)
	}
}

func TestSandboxAcceptsMoreThan150DefinitionsAndNestedChoices(t *testing.T) {
	options := make([]map[string]any, 164)
	for id := range options {
		options[id] = map[string]any{"id": id, "options": map[string]any{"choices": []string{"off", "on"}}}
	}
	payload, _ := json.Marshal(map[string]any{"options": options})
	if err := ValidateSandboxEdits(payload, map[int]int{163: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxDuplicateReportsSecondRecord(t *testing.T) {
	_, err := ParseSandboxCode("AAAAAAA")
	var parseErr *SandboxParseError
	if !errors.As(err, &parseErr) || parseErr.Record != 1 {
		t.Fatalf("err=%#v", err)
	}
}

func TestSandboxDuplicateKeepsLaterRecordsInOrder(t *testing.T) {
	code, err := ParseSandboxCode("AAAAAAAGHL")
	if len(code.Records) != 3 || code.Records[2] != (SandboxRecord{OptionID: 163, ValueIndex: 11}) {
		t.Fatalf("records=%#v", code.Records)
	}
	if !errors.Is(err, ErrSandboxDuplicateOption) {
		t.Fatalf("err=%v", err)
	}
}

func TestDashboardFetchValidatesFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/sandbox-api-v3.1-b14.json")
	if err != nil {
		t.Fatal(err)
	}
	var requests int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-SDTD-API-TOKENNAME"); got != "panel" {
			t.Fatalf("token name=%q", got)
		}
		if got := r.Header.Get("X-SDTD-API-SECRET"); got != "secret" {
			t.Fatalf("token secret=%q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("unexpected Authorization=%q", got)
		}
		switch r.URL.Path {
		case "/api/openapi/openapi.yaml":
			requests++
			_, _ = w.Write([]byte("paths:\n  /api/serverstate/SandboxSettings:\n    get:\n      operationId: SandboxSettings\n"))
		case "/api/serverstate/SandboxSettings":
			if r.URL.Query().Get("code") != friendCode || r.URL.Query().Get("detailed") != "true" {
				t.Errorf("query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write(fixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := DashboardClient{BaseURL: s.URL, TokenName: "panel", TokenSecret: "secret", HTTP: s.Client()}
	if _, err := c.FetchSandbox(context.Background(), friendCode, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FetchSandbox(context.Background(), friendCode, true); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("openapi requests=%d", requests)
	}
}

func TestDashboardFetchRediscoversPathWhenBuildChanges(t *testing.T) {
	var openAPIRequests, oldRequests, newRequests int
	build := "A"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/openapi/openapi.yaml":
			openAPIRequests++
			if build == "A" {
				_, _ = w.Write([]byte("paths:\n  /SandboxSettingsA:\n    get: {}\n"))
			} else {
				_, _ = w.Write([]byte("paths:\n  /SandboxSettingsB:\n    get: {}\n"))
			}
		case "/SandboxSettingsA":
			oldRequests++
			_, _ = w.Write([]byte(`{"build":"` + build + `","code":"A","options":[{"id":0,"values":["off","on"]}]}`))
		case "/SandboxSettingsB":
			newRequests++
			_, _ = w.Write([]byte(`{"build":"B","code":"A","options":[{"id":0,"values":["off","on"]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := DashboardClient{BaseURL: s.URL, HTTP: s.Client()}
	if _, err := c.FetchSandbox(context.Background(), "A", true); err != nil {
		t.Fatal(err)
	}
	build = "B"
	if _, err := c.FetchSandbox(context.Background(), "A", true); err != nil {
		t.Fatal(err)
	}
	if openAPIRequests != 2 || oldRequests != 2 || newRequests != 1 {
		t.Fatalf("openapi=%d old=%d new=%d", openAPIRequests, oldRequests, newRequests)
	}
}

func TestDashboardFetchRejectsMissingOptionsAndAuth(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/openapi/openapi.yaml" {
				_, _ = w.Write([]byte("paths:\n  /SandboxSettings:\n    get: {}\n"))
				return
			}
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = w.Write([]byte(`{"code":"A","build":"V3.1.0-b14"}`))
			}
		}))
		_, err := (DashboardClient{BaseURL: s.URL, HTTP: s.Client()}).FetchSandbox(context.Background(), "A", false)
		s.Close()
		if status == http.StatusUnauthorized && !errors.Is(err, ErrDashboardAuthRequired) {
			t.Fatalf("auth error=%v", err)
		}
		if status == http.StatusOK && err == nil {
			t.Fatal("accepted missing options")
		}
	}
}

func TestDashboardFetchRejectsNonArrayOptions(t *testing.T) {
	for _, options := range []string{"null", "{}"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/openapi/openapi.yaml" {
				_, _ = w.Write([]byte("paths:\n  /SandboxSettings:\n    get: {}\n"))
				return
			}
			_, _ = w.Write([]byte(`{"code":"A","options":` + options + `}`))
		}))
		_, err := (DashboardClient{BaseURL: s.URL, HTTP: s.Client()}).FetchSandbox(context.Background(), "A", true)
		s.Close()
		if err == nil {
			t.Fatalf("accepted options=%s", options)
		}
	}
}

func TestSandboxCacheUsesMatchingBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "sandbox.json")
	payload := json.RawMessage(`{"build":"V3.1.0-b14","code":"A","options":[{"id":0,"values":["off","on"]}]}`)
	if err := SaveSandboxCache(path, "V3.1.0-b14", payload); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSandboxCache(path, "V3.1.0-b14")
	if err != nil || string(got.Payload) != string(payload) {
		t.Fatalf("cache=%s err=%v", got.Payload, err)
	}
	if _, err := LoadSandboxCache(path, "other"); !errors.Is(err, ErrSandboxCacheBuildMismatch) {
		t.Fatalf("mismatch=%v", err)
	}
}

func TestSandboxCacheAcceptsOfficialDefinitionsWithoutChoiceMetadata(t *testing.T) {
	payload := json.RawMessage(`{"build":"V3.1.0-b14","code":"A","options":[{"key":"BlockDamage"}]}`)
	if err := SaveSandboxCache(filepath.Join(t.TempDir(), "sandbox.json"), "V3.1.0-b14", payload); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardFetchRejectsNullOption(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/openapi/openapi.yaml" {
			_, _ = w.Write([]byte("paths:\n  /SandboxSettings:\n    get: {}\n"))
			return
		}
		_, _ = w.Write([]byte(`{"code":"A","options":[null]}`))
	}))
	defer s.Close()
	if _, err := (DashboardClient{BaseURL: s.URL, HTTP: s.Client()}).FetchSandbox(context.Background(), "A", true); err == nil {
		t.Fatal("accepted null option")
	}
}

func TestSandboxCacheRequiresExactKnownBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sandbox.json")
	payload := json.RawMessage(`{"build":"V3.1.0-b14","code":"A","options":[{"id":0,"values":["off","on"]}]}`)
	if err := SaveSandboxCache(path, "V3.1.0-b14", payload); err != nil {
		t.Fatal(err)
	}
	for _, build := range []string{"v3.1.0-b14", " V3.1.0-b14", "V3.1.0-b14 ", "unknown"} {
		if _, err := LoadSandboxCache(path, build); !errors.Is(err, ErrSandboxCacheBuildMismatch) {
			t.Fatalf("build %q err=%v", build, err)
		}
	}
}

func TestSandboxRawForceSavesVerbatim(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(filepath.Dir(a.paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.ServerConfig, []byte("<ServerSettings><property name=\"SandboxCode\" value=\"A\" /></ServerSettings>"), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"hash":"` + doc.Hash + `","code":"AA<bad","force":true}`
	req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8787/api/sandbox/raw", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("X-Panel-Token", a.token)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
	if !strings.Contains(string(mustRead(t, a.paths.ServerConfig)), "AA&lt;bad") {
		t.Fatal("raw code was not XML escaped")
	}
}

func TestValidateSandboxEditsRejectsUntrustedEdits(t *testing.T) {
	for _, test := range []struct {
		name    string
		options string
		edits   string
	}{
		{"unknown option", `[{"id":0,"valueSet":["off","on"]}]`, `{"99":1}`},
		{"missing option id", `[{"name":"missing"}]`, `{"0":1}`},
		{"missing values", `[{"id":0}]`, `{"0":1}`},
		{"empty valueSet", `[{"id":0,"valueSet":[]}]`, `{"0":1}`},
		{"null valueSet", `[{"id":0,"valueSet":null}]`, `{"0":1}`},
		{"object valueSet", `[{"id":0,"valueSet":{"0":"off"}}]`, `{"0":1}`},
		{"details are not values", `[{"id":0,"details":[{"value":0},{"value":1}]}]`, `{"0":1}`},
		{"value above encoding", `[{"id":0,"valueSet":[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25]}]`, `{"0":26}`},
		{"value outside valueSet", `[{"id":"0","valueSet":["off","on"]}]`, `{"0":2}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var edits map[int]int
			if err := json.Unmarshal([]byte(test.edits), &edits); err != nil {
				t.Fatal(err)
			}
			payload := json.RawMessage(`{"options":` + test.options + `}`)
			if err := ValidateSandboxEdits(payload, edits); err == nil {
				t.Fatal("accepted untrusted edit")
			}
		})
	}
}

func TestSandboxOptionsValidEditPreservesEveryOtherRecord(t *testing.T) {
	a, hash := sandboxOptionsApp(t, "V3.1.0-b14", `[{"optionId":"0","values":[0,1,2,3,4,5,6,7,8,9]}]`)
	rr := putSandboxOptions(t, a, hash, `{"0":9}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := configValue(doc, "SandboxCode")
	got, err := ParseSandboxCode(raw)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ParseSandboxCode(friendCode)
	if len(got.Records) != 37 || got.ValueOf(0) != 9 || got.ValueOf(163) != 11 {
		t.Fatalf("records=%d option0=%d option163=%d", len(got.Records), got.ValueOf(0), got.ValueOf(163))
	}
	for _, record := range want.Records {
		if record.OptionID != 0 && got.ValueOf(record.OptionID) != record.ValueIndex {
			t.Fatalf("option %d changed from %d to %d", record.OptionID, record.ValueIndex, got.ValueOf(record.OptionID))
		}
	}
}

func TestSandboxOptionsRejectsBuildMismatch(t *testing.T) {
	for _, build := range []string{"unknown", "V3.2.0-b1", "v3.1.0-b14", " V3.1.0-b14", "V3.1.0-b14 "} {
		t.Run(build, func(t *testing.T) {
			a, hash := sandboxOptionsApp(t, build, `[{"key":0,"valueSet":["off","on"]}]`)
			rr := putSandboxOptions(t, a, hash, `{"0":1}`)
			if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "sandbox_build_mismatch") {
				t.Fatalf("got %d: %s", rr.Code, rr.Body)
			}
		})
	}
}

func sandboxOptionsApp(t *testing.T, serverBuild, options string) (*App, string) {
	t.Helper()
	a := newTestApp(t)
	if err := os.MkdirAll(filepath.Dir(a.paths.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.paths.ServerConfig, []byte(`<ServerSettings><property name="SandboxCode" value="`+friendCode+`" /></ServerSettings>`), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"build":"V3.1.0-b14","code":"` + friendCode + `","options":` + options + `}`)
	if err := SaveSandboxCache(a.paths.SandboxCache, "V3.1.0-b14", payload); err != nil {
		t.Fatal(err)
	}
	a.server.mu.Lock()
	a.server.build = serverBuild
	a.server.mu.Unlock()
	return a, doc.Hash
}

func putSandboxOptions(t *testing.T, a *App, hash, edits string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8787/api/sandbox/options", strings.NewReader(`{"hash":"`+hash+`","edits":`+edits+`}`))
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("X-Panel-Token", a.token)
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	return rr
}

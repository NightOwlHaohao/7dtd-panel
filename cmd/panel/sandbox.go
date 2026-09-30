package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrSandboxInvalidCode        = errors.New("invalid SandboxCode")
	ErrSandboxDuplicateOption    = errors.New("duplicate SandboxCode option")
	ErrDashboardAuthRequired     = errors.New("Dashboard session token required")
	ErrSandboxCacheBuildMismatch = errors.New("sandbox cache build mismatch")
)

type SandboxRecord struct{ OptionID, ValueIndex int }

type SandboxCode struct {
	Header  byte
	Records []SandboxRecord
}

type SandboxParseError struct {
	Offset int
	Record int
	Reason string
	Kind   error
}

func (e *SandboxParseError) Error() string {
	return fmt.Sprintf("SandboxCode record %d at character %d: %s", e.Record, e.Offset+1, e.Reason)
}

func (e *SandboxParseError) Unwrap() error { return e.Kind }

func ParseSandboxCode(code string) (SandboxCode, error) {
	code = strings.ReplaceAll(strings.TrimSpace(code), "\r", "")
	code = strings.ReplaceAll(code, "\n", "")
	if len(code) < 1 || (len(code)-1)%3 != 0 {
		return SandboxCode{}, &SandboxParseError{Offset: len(code), Record: len(code) / 3, Reason: "invalid length", Kind: ErrSandboxInvalidCode}
	}
	for i := range code {
		if code[i] < 'A' || code[i] > 'Z' {
			record := 0
			if i > 0 {
				record = (i - 1) / 3
			}
			return SandboxCode{}, &SandboxParseError{Offset: i, Record: record, Reason: "invalid character", Kind: ErrSandboxInvalidCode}
		}
	}
	c := SandboxCode{Header: code[0], Records: make([]SandboxRecord, 0, (len(code)-1)/3)}
	seen := make(map[int]bool)
	var parseErr *SandboxParseError
	for i := 1; i < len(code); i += 3 {
		id := int(code[i]-'A')*26 + int(code[i+1]-'A')
		c.Records = append(c.Records, SandboxRecord{id, int(code[i+2] - 'A')})
		if seen[id] && parseErr == nil {
			parseErr = &SandboxParseError{Offset: i, Record: (i - 1) / 3, Reason: fmt.Sprintf("duplicate option %d", id), Kind: ErrSandboxDuplicateOption}
		}
		seen[id] = true
	}
	if parseErr != nil {
		return c, parseErr
	}
	return c, nil
}

func (c SandboxCode) String() string {
	b := make([]byte, 1, 1+len(c.Records)*3)
	b[0] = c.Header
	for _, r := range c.Records {
		if r.OptionID < 0 || r.OptionID >= 26*26 || r.ValueIndex < 0 || r.ValueIndex >= 26 {
			continue
		}
		b = append(b, byte(r.OptionID/26)+'A', byte(r.OptionID%26)+'A', byte(r.ValueIndex)+'A')
	}
	return string(b)
}

func (c SandboxCode) ValueOf(id int) int {
	for _, r := range c.Records {
		if r.OptionID == id {
			return r.ValueIndex
		}
	}
	return -1
}

func MergeSandbox(base SandboxCode, edits map[int]int) SandboxCode {
	updated := make(map[int]bool, len(edits))
	for i := range base.Records {
		if value, ok := edits[base.Records[i].OptionID]; ok && value >= 0 && value < 26 {
			base.Records[i].ValueIndex, updated[base.Records[i].OptionID] = value, true
		}
	}
	ids := make([]int, 0, len(edits))
	for id, value := range edits {
		if !updated[id] && id >= 0 && id < 26*26 && value >= 0 && value < 26 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		base.Records = append(base.Records, SandboxRecord{id, edits[id]})
	}
	return base
}

type DashboardClient struct {
	BaseURL, TokenName, TokenSecret string
	HTTP                            *http.Client
}

func (c DashboardClient) authorize(req *http.Request) {
	if c.TokenName != "" {
		req.Header.Set("X-SDTD-API-TOKENNAME", c.TokenName)
	}
	if c.TokenSecret != "" {
		req.Header.Set("X-SDTD-API-SECRET", c.TokenSecret)
	}
}

type dashboardPathEntry struct{ path, build string }

var dashboardPaths sync.Map // BaseURL -> discovered SandboxSettings path and its response build.
var openAPIPathLine = regexp.MustCompile(`^\s*(/[^\s:]+):\s*(?:#.*)?$`)

func (c DashboardClient) FetchSandbox(ctx context.Context, code string, detailed bool) (json.RawMessage, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	entry, cached := dashboardPaths.Load(base)
	path := ""
	var err error
	if cached {
		path = entry.(dashboardPathEntry).path
	} else {
		path, err = c.sandboxPath(ctx)
		if err != nil {
			return nil, err
		}
	}
	payload, err := c.fetchSandbox(ctx, path, code, detailed)
	if err != nil && cached {
		dashboardPaths.Delete(base)
		path, err = c.sandboxPath(ctx)
		if err != nil {
			return nil, err
		}
		payload, err = c.fetchSandbox(ctx, path, code, detailed)
	}
	if err != nil {
		return nil, err
	}
	build := sandboxPayloadBuild(payload)
	if cached && entry.(dashboardPathEntry).build != build {
		dashboardPaths.Delete(base)
		path, err = c.sandboxPath(ctx)
		if err != nil {
			return nil, err
		}
		if payload, err = c.fetchSandbox(ctx, path, code, detailed); err != nil {
			return nil, err
		}
		build = sandboxPayloadBuild(payload)
	}
	dashboardPaths.Store(base, dashboardPathEntry{path, build})
	return payload, nil
}

func (c DashboardClient) fetchSandbox(ctx context.Context, path, code string, detailed bool) (json.RawMessage, error) {
	u, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("code", code)
	q.Set("detailed", fmt.Sprint(detailed))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrDashboardAuthRequired
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Dashboard SandboxSettings returned HTTP %d", resp.StatusCode)
	}
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	payload, err = normalizeSandboxPayload(payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(payload), nil
}

func (c DashboardClient) sandboxPath(ctx context.Context) (string, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/openapi/openapi.yaml", nil)
	if err != nil {
		return "", err
	}
	c.authorize(req)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrDashboardAuthRequired
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Dashboard OpenAPI returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	path, err := discoverSandboxPath(string(b))
	if err != nil {
		return "", err
	}
	return path, nil
}

func discoverSandboxPath(openAPI string) (string, error) {
	lines := strings.Split(openAPI, "\n")
	for i, line := range lines {
		m := openAPIPathLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		path, block := m[1], ""
		for j := i + 1; j < len(lines) && openAPIPathLine.FindStringSubmatch(lines[j]) == nil; j++ {
			block += strings.ToLower(lines[j]) + "\n"
		}
		if strings.Contains(strings.ToLower(path), "sandboxsettings") {
			// The 3.1 Dashboard root document delegates this operation's GET
			// declaration to SandboxSettings.openapi.yaml via $ref.
			return path, nil
		}
		if strings.Contains(block, "get:") && strings.Contains(block, "sandboxsettings") {
			return path, nil
		}
	}
	return "", errors.New("SandboxSettings GET path not found in OpenAPI")
}

func validateSandboxPayload(payload []byte) error {
	_, err := normalizeSandboxPayload(payload)
	return err
}

// normalizeSandboxPayload removes the standard WebAPI {data,meta} envelope.
// The rest of the panel deliberately consumes only the documented data object.
func normalizeSandboxPayload(payload []byte) ([]byte, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	if data := value["data"]; len(data) != 0 {
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, errors.New("SandboxSettings response has invalid data")
		}
	}
	if value["code"] == nil || value["options"] == nil || !strings.HasPrefix(strings.TrimSpace(string(value["options"])), "[") {
		return nil, errors.New("SandboxSettings response needs code and options")
	}
	var code string
	var options []json.RawMessage
	if err := json.Unmarshal(value["code"], &code); err != nil || json.Unmarshal(value["options"], &options) != nil {
		return nil, errors.New("SandboxSettings response has invalid code or options")
	}
	if len(options) == 0 {
		return nil, errors.New("SandboxSettings response has no usable options")
	}
	for index, raw := range options {
		var option map[string]json.RawMessage
		if json.Unmarshal(raw, &option) != nil || option == nil {
			return nil, errors.New("SandboxSettings response has malformed option")
		}
		id, ok := sandboxOptionID(option)
		if !ok {
			// The official 3.1 response uses a textual key and defines the
			// SandboxCode position by response order.
			option["id"], _ = json.Marshal(index)
			options[index], _ = json.Marshal(option)
			continue
		}
		if id < 0 || id >= 26*26 {
			return nil, errors.New("SandboxSettings response has untrusted option")
		}
	}
	value["options"], _ = json.Marshal(options)
	return json.Marshal(value)
}

func ValidateSandboxEdits(payload json.RawMessage, edits map[int]int) error {
	var root struct {
		Options []json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(payload, &root); err != nil || root.Options == nil {
		return errors.New("Sandbox definitions have invalid options")
	}
	limits := make(map[int]int)
	for _, raw := range root.Options {
		var option map[string]json.RawMessage
		if json.Unmarshal(raw, &option) != nil {
			continue
		}
		id, ok := sandboxOptionID(option)
		if !ok || id < 0 || id >= 26*26 {
			continue
		}
		values, editable := sandboxChoices(option)
		limit := len(values)
		if !editable {
			limit = 0
		}
		if previous, exists := limits[id]; !exists || (previous > 0 && limit < previous) {
			limits[id] = limit
		}
	}
	for id, value := range edits {
		limit, ok := limits[id]
		if !ok {
			return fmt.Errorf("Sandbox option %d is not in the current definitions", id)
		}
		if value < 0 || value >= 26 {
			return fmt.Errorf("Sandbox option %d value %d is out of range", id, value)
		}
		if limit == 0 {
			return fmt.Errorf("Sandbox option %d has no recognized non-empty value list", id)
		}
		if value >= limit {
			return fmt.Errorf("Sandbox option %d value %d exceeds its value list", id, value)
		}
	}
	return nil
}

func sandboxOptionID(option map[string]json.RawMessage) (int, bool) {
	for _, field := range []string{"id", "optionId", "key"} {
		raw, exists := option[field]
		if !exists || string(raw) == "null" {
			continue
		}
		var id int
		if json.Unmarshal(raw, &id) == nil {
			return id, true
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			if id, err := strconv.Atoi(strings.TrimSpace(text)); err == nil {
				return id, true
			}
		}
		return 0, false
	}
	return 0, false
}

func sandboxOptionTexts(payload json.RawMessage, catalog LocalizationCatalog) map[string]LocalizedText {
	var root struct {
		Options []map[string]json.RawMessage `json:"options"`
	}
	if json.Unmarshal(payload, &root) != nil {
		return nil
	}
	texts := make(map[string]LocalizedText)
	for _, option := range root.Options {
		id, ok := sandboxOptionID(option)
		if !ok {
			continue
		}
		var name string
		for _, key := range []string{"property", "propertyName", "name"} {
			if json.Unmarshal(option[key], &name) == nil && name != "" {
				break
			}
		}
		if name != "" {
			texts[strconv.Itoa(id)] = catalog.ConfigText(name)
		}
	}
	return texts
}

func sandboxChoices(option map[string]json.RawMessage) ([]json.RawMessage, bool) {
	for _, field := range []string{"valueSet", "values", "options"} {
		raw, exists := option[field]
		if !exists {
			continue
		}
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) == nil && len(values) > 0 {
			return values, true
		}
		if field != "options" {
			continue
		}
		var nested struct {
			Choices []json.RawMessage `json:"choices"`
		}
		if json.Unmarshal(raw, &nested) == nil && len(nested.Choices) > 0 {
			return nested.Choices, true
		}
	}
	return nil, false
}

type SandboxCache struct {
	Build     string          `json:"build"`
	FetchedAt time.Time       `json:"fetchedAt"`
	Payload   json.RawMessage `json:"payload"`
}

func knownSandboxBuild(build string) bool {
	return build != "" && build != "unknown"
}

func LoadSandboxCache(path, build string) (SandboxCache, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return SandboxCache{}, err
	}
	var cache SandboxCache
	if err := json.Unmarshal(b, &cache); err != nil {
		return SandboxCache{}, err
	}
	if !knownSandboxBuild(build) || !knownSandboxBuild(cache.Build) || cache.Build != build {
		return SandboxCache{}, ErrSandboxCacheBuildMismatch
	}
	if sandboxPayloadBuild(cache.Payload) != build {
		return SandboxCache{}, ErrSandboxCacheBuildMismatch
	}
	if err := validateSandboxPayload(cache.Payload); err != nil {
		return SandboxCache{}, err
	}
	return cache, nil
}

func SaveSandboxCache(path, build string, payload json.RawMessage) error {
	if !knownSandboxBuild(build) {
		return ErrSandboxCacheBuildMismatch
	}
	if sandboxPayloadBuild(payload) != build {
		return ErrSandboxCacheBuildMismatch
	}
	if err := validateSandboxPayload(payload); err != nil {
		return err
	}
	b, err := json.Marshal(SandboxCache{Build: build, FetchedAt: time.Now().UTC(), Payload: payload})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".sandbox-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(b); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(tempPath, path)
}

func sandboxPayloadBuild(payload json.RawMessage) string {
	var value struct {
		Build string `json:"build"`
	}
	_ = json.Unmarshal(payload, &value)
	return value.Build
}

func withSandboxPayloadBuild(payload json.RawMessage, build string) (json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(build)
	if err != nil {
		return nil, err
	}
	value["build"] = encoded
	return json.Marshal(value)
}

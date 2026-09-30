package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	ErrConfigChanged    = errors.New("serverconfig.xml 已被外部修改")
	ErrPropertyNotFound = errors.New("serverconfig.xml 中找不到属性")
)

// ponytail: one server needs one lock; use path-keyed locks if parallel multi-server saves matter.
var configSaveMu sync.Mutex

type ConfigProperty struct {
	Name           string        `json:"name"`
	Value          string        `json:"value"`
	Secret         bool          `json:"secret,omitempty"`
	EnglishComment string        `json:"englishComment,omitempty"`
	Text           LocalizedText `json:"text,omitempty"`
}

type ConfigDocument struct {
	Hash       string           `json:"hash"`
	Properties []ConfigProperty `json:"properties"`
}

func LoadConfig(path string) (ConfigDocument, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ConfigDocument{}, err
	}
	properties, err := parseConfig(b)
	if err != nil {
		return ConfigDocument{}, err
	}
	return ConfigDocument{Hash: configHash(b), Properties: properties}, nil
}

func SaveConfig(path, backupDir, expectedHash string, updates map[string]string) (string, error) {
	return saveConfig(path, backupDir, expectedHash, updates, false)
}

func EnsureUserDataFolder(paths Paths, expectedHash string) (string, error) {
	value, err := filepath.Abs(paths.UserData)
	if err != nil {
		return "", err
	}
	return saveConfig(paths.ServerConfig, paths.ConfigBackups, expectedHash, map[string]string{"UserDataFolder": value}, true)
}

func saveConfig(path, backupDir, expectedHash string, updates map[string]string, insertUserData bool) (string, error) {
	configSaveMu.Lock()
	defer configSaveMu.Unlock()

	original, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if configHash(original) != expectedHash {
		return "", ErrConfigChanged
	}
	if _, err := parseConfig(original); err != nil {
		return "", err
	}
	updated, err := rewriteConfig(original, updates, insertUserData)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", err
	}
	backup := filepath.Join(backupDir, time.Now().Format("20060102-150405.000000000")+".xml")
	if err := os.WriteFile(backup, original, 0600); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".serverconfig-*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(updated); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	tempBytes, err := os.ReadFile(tempPath)
	if err != nil {
		return "", err
	}
	if _, err := parseConfig(tempBytes); err != nil {
		return "", err
	}
	if err := replaceFile(tempPath, path); err != nil {
		return "", err
	}
	return configHash(tempBytes), nil
}

func parseConfig(b []byte) ([]ConfigProperty, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	var properties []ConfigProperty
	lastProperty := -1
	for {
		token, err := d.Token()
		if err == io.EOF {
			return properties, nil
		}
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local != "property" {
				lastProperty = -1
				continue
			}
			lastProperty = -1
			var p ConfigProperty
			for _, attr := range value.Attr {
				switch attr.Name.Local {
				case "name":
					p.Name = attr.Value
				case "value":
					p.Value = attr.Value
				}
			}
			if p.Name != "" {
				p.Secret = isSecret(p.Name)
				if p.Secret {
					p.Value = "********"
				}
				properties = append(properties, p)
				lastProperty = len(properties) - 1
			}
		case xml.Comment:
			comment := strings.TrimSpace(string(value))
			if lastProperty >= 0 && comment != "" {
				properties[lastProperty].EnglishComment = comment
			}
			lastProperty = -1
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				lastProperty = -1
			}
		}
	}
}

// rewriteConfig edits only the value attributes being changed, leaving every
// other byte (comments, whitespace, self-closing tags, quoting) as written.
func rewriteConfig(b []byte, updates map[string]string, insertUserData bool) ([]byte, error) {
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	found := make(map[string]bool, len(updates))
	d := xml.NewDecoder(bytes.NewReader(b))
	depth, lastIndent := 0, "\t"
	for {
		start := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		end := int(d.InputOffset())
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if value.Name.Local != "property" {
				continue
			}
			lastIndent = lineIndent(b, start)
			name := attrValue(value.Attr, "name")
			update, ok := updates[name]
			if !ok {
				continue
			}
			tag, err := setTagAttribute(b[start:end], "value", update)
			if err != nil {
				return nil, fmt.Errorf("property %s: %w", name, err)
			}
			edits = append(edits, edit{start, end, tag})
			found[name] = true
		case xml.EndElement:
			if depth == 1 && insertUserData && !found["UserDataFolder"] {
				line := lastIndent + `<property name="UserDataFolder" value="` + escapeAttribute(updates["UserDataFolder"]) + `" />` + "\n"
				at := start
				for at > 0 && (b[at-1] == ' ' || b[at-1] == '\t') {
					at-- // insert before the closing tag's own indentation
				}
				if at > 0 && b[at-1] != '\n' {
					line = "\n" + line
				}
				edits = append(edits, edit{at, at, line})
				found["UserDataFolder"] = true
			}
			depth--
		}
	}
	for name := range updates {
		if !found[name] {
			return nil, fmt.Errorf("%w: %s", ErrPropertyNotFound, name)
		}
	}
	out := append([]byte(nil), b...)
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		out = append(out[:e.start:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	return out, nil
}

var tagAttribute = regexp.MustCompile(`\s([A-Za-z_:][-A-Za-z0-9_:.]*)\s*=\s*("[^"]*"|'[^']*')`)

// setTagAttribute replaces (or appends) one attribute in a raw start tag,
// keeping the tag's other attributes, spacing and self-closing form.
func setTagAttribute(tag []byte, name, value string) (string, error) {
	text := string(tag)
	for _, match := range tagAttribute.FindAllStringSubmatchIndex(text, -1) {
		if text[match[2]:match[3]] == name {
			return text[:match[4]+1] + escapeAttribute(value) + text[match[5]-1:], nil
		}
	}
	close := strings.LastIndex(text, ">")
	if close < 0 {
		return "", errors.New("malformed start tag")
	}
	if close > 0 && text[close-1] == '/' {
		close--
		for close > 0 && text[close-1] == ' ' {
			close--
		}
	}
	return text[:close] + " " + name + `="` + escapeAttribute(value) + `"` + text[close:], nil
}

func escapeAttribute(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}

// lineIndent returns the whitespace between the start of the line and offset.
func lineIndent(b []byte, offset int) string {
	start := offset
	for start > 0 && (b[start-1] == ' ' || b[start-1] == '\t') {
		start--
	}
	if start > 0 && b[start-1] != '\n' {
		return "\t"
	}
	return string(b[start:offset])
}

func attrValue(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func isSecret(name string) bool  { return strings.Contains(strings.ToLower(name), "password") }
func configHash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

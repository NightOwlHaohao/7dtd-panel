package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func copyFixture(t *testing.T, name, dir string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filepath.Base(name))
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSaveConfigBacksUpAndPreservesUnknownContent(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, "testdata/serverconfig.xml", dir)
	doc, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := SaveConfig(path, filepath.Join(dir, "backups"), doc.Hash, map[string]string{"ServerName": "新服务器"})
	if err != nil {
		t.Fatal(err)
	}
	if newHash == doc.Hash {
		t.Fatal("hash did not change")
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{"新服务器", "未知参数", "保留此注释"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("missing %q", want)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "backups", "*.xml"))
	if len(matches) != 1 {
		t.Fatalf("backup count=%d", len(matches))
	}
}

func TestLoadConfigMasksPassword(t *testing.T) {
	path := copyFixture(t, "testdata/serverconfig.xml", t.TempDir())
	doc, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, property := range doc.Properties {
		if property.Name == "TelnetPassword" && (!property.Secret || property.Value != "********") {
			t.Fatalf("password exposed: %+v", property)
		}
	}
}

func TestParseConfigAttachesFollowingEnglishComment(t *testing.T) {
	doc, err := parseConfig([]byte(`<ServerSettings><property name="ServerPort" value="26900" /><!-- Port used by the game server. --></ServerSettings>`))
	if err != nil {
		t.Fatal(err)
	}
	if doc[0].EnglishComment != "Port used by the game server." {
		t.Fatalf("comment=%q", doc[0].EnglishComment)
	}
}

func TestEnrichConfigPreservesConfigData(t *testing.T) {
	doc := ConfigDocument{Hash: "hash", Properties: []ConfigProperty{{Name: "ServerPort", Value: "26900", EnglishComment: "Port."}}}
	got := EnrichConfig(doc, LocalizationCatalog{})
	if got.Hash != "hash" || got.Properties[0].Value != "26900" || got.Properties[0].EnglishComment != "Port." || got.Properties[0].Text.Labels["en"] != "Server Port" {
		t.Fatalf("got=%#v", got)
	}
}

func TestSaveConfigRejectsStaleHash(t *testing.T) {
	dir := t.TempDir()
	path := copyFixture(t, "testdata/serverconfig.xml", dir)
	doc, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(" "), mustRead(t, path)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveConfig(path, filepath.Join(dir, "backups"), doc.Hash, map[string]string{"ServerName": "新服务器"}); !errors.Is(err, ErrConfigChanged) {
		t.Fatalf("got %v, want ErrConfigChanged", err)
	}
}

func TestSaveConfigInvalidXMLLeavesOriginalBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serverconfig.xml")
	original := []byte("<ServerSettings><property>")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveConfig(path, filepath.Join(dir, "backups"), configHash(original), map[string]string{"ServerName": "新服务器"}); err == nil {
		t.Fatal("expected invalid XML error")
	}
	if actual := mustRead(t, path); !bytes.Equal(actual, original) {
		t.Fatal("original changed")
	}
}

func TestSaveConfigSerializesConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "serverconfig.xml")
	original := []byte(`<ServerSettings><property name="ServerName" value="old"/><property name="SandboxCode" value="old"/>` + strings.Repeat(`<property name="Padding" value="x"/>`, 50000) + `</ServerSettings>`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(dir, "backups")
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, updates := range []map[string]string{
		{"ServerName": "first"},
		{"SandboxCode": "second"},
	} {
		updates := updates
		go func() {
			<-start
			_, err := SaveConfig(path, backupDir, doc.Hash, updates)
			errs <- err
		}()
	}
	close(start)
	succeeded, changed := 0, 0
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConfigChanged):
			changed++
		default:
			t.Fatalf("unexpected save error: %v", err)
		}
	}
	if succeeded != 1 || changed != 1 {
		t.Fatalf("successes=%d config-changed=%d", succeeded, changed)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("final config is invalid: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(backupDir, "*.xml"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v err=%v", backups, err)
	}
	if backup := mustRead(t, backups[0]); !bytes.Equal(backup, original) {
		t.Fatal("backup was overwritten")
	}
}

func TestEnsureUserDataFolderAfterRootMove(t *testing.T) {
	first := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(first.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, "testdata/serverconfig.xml", filepath.Dir(first.ServerConfig))
	doc, err := LoadConfig(first.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureUserDataFolder(first, doc.Hash); err != nil {
		t.Fatal(err)
	}

	second := ResolvePaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(second.ServerConfig), 0700); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, "testdata/serverconfig.xml", filepath.Dir(second.ServerConfig))
	doc, err = LoadConfig(second.ServerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureUserDataFolder(second, doc.Hash); err != nil {
		t.Fatal(err)
	}
	b := mustRead(t, second.ServerConfig)
	absolute, err := filepath.Abs(second.UserData)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(absolute)) || bytes.Contains(b, []byte(first.UserData)) {
		t.Fatalf("userdata path not updated: %s", b)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRewriteConfigChangesOnlyTheEditedValues(t *testing.T) {
	original := "<?xml version=\"1.0\"?>\r\n<ServerSettings>\r\n\t<!-- keep <this> comment -->\r\n\t<property name=\"ServerName\"   value=\"Old &amp; Grey\" />\r\n\t<property name='ServerPort' value='26900'/>\r\n\t<property name=\"Region\" value=\"Asia\"></property>\r\n</ServerSettings>\r\n"
	got, err := rewriteConfig([]byte(original), map[string]string{"ServerName": `New "Wasteland" <3`, "ServerPort": "27000"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(original, `value="Old &amp; Grey"`, `value="New &#34;Wasteland&#34; &lt;3"`, 1)
	want = strings.Replace(want, `value='26900'`, `value='27000'`, 1)
	if string(got) != want {
		t.Fatalf("rewrite changed more than the values:\n got %q\nwant %q", got, want)
	}
	doc, err := parseConfig(got)
	if err != nil {
		t.Fatal(err)
	}
	if doc[0].Value != `New "Wasteland" <3` {
		t.Fatalf("round-trip value = %q", doc[0].Value)
	}
}

func TestRewriteConfigAddsMissingValueAndInsertsUserData(t *testing.T) {
	original := "<ServerSettings>\n    <property name=\"GameName\"/>\n</ServerSettings>\n"
	got, err := rewriteConfig([]byte(original), map[string]string{"GameName": "MyGame", "UserDataFolder": `C:\Panel\userdata`}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "<ServerSettings>\n    <property name=\"GameName\" value=\"MyGame\"/>\n    <property name=\"UserDataFolder\" value=\"C:\\Panel\\userdata\" />\n</ServerSettings>\n"
	if string(got) != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if _, err := rewriteConfig([]byte(original), map[string]string{"Missing": "x"}, false); !errors.Is(err, ErrPropertyNotFound) {
		t.Fatalf("missing property err=%v", err)
	}
}

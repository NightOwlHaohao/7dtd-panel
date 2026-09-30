//go:build windows

package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestServiceExecutableParsesCommandLines(t *testing.T) {
	for commandLine, want := range map[string]string{
		`"C:\Panel Files\panel.exe"`:           `C:\Panel Files\panel.exe`,
		`"C:\Panel Files\panel.exe" --console`: `C:\Panel Files\panel.exe`,
		`C:\panel\panel.exe`:                   `C:\panel\panel.exe`,
		`C:\panel\panel.exe service`:           `C:\panel\panel.exe`,
		`  "C:\unterminated\panel.exe`:         `C:\unterminated\panel.exe`,
	} {
		if got := serviceExecutable(commandLine); got != want {
			t.Errorf("serviceExecutable(%q) = %q, want %q", commandLine, got, want)
		}
	}
}

func TestServiceSDDLGrantsOnlyTheIntendedRights(t *testing.T) {
	const serviceSID = "S-1-5-80-1"
	rights := map[string][]string{}
	for _, ace := range regexp.MustCompile(`\(A;;([A-Z]+);;;([^)]+)\)`).FindAllStringSubmatch(serviceSDDL(serviceSID), -1) {
		for i := 0; i+2 <= len(ace[1]); i += 2 {
			rights[ace[2]] = append(rights[ace[2]], ace[1][i:i+2])
		}
	}
	has := func(trustee, right string) bool { return slices.Contains(rights[trustee], right) }
	for _, right := range []string{"LC", "RP", "WP"} {
		if !has("IU", right) {
			t.Errorf("interactive users need %s to query, start and stop the panel", right)
		}
	}
	if !has(serviceSID, "SD") {
		t.Error("the service must be able to delete its own registration")
	}
	for trustee := range rights {
		if trustee != "BA" && (has(trustee, "DC") || has(trustee, "WD") || has(trustee, "WO")) {
			t.Errorf("%s may change the service configuration or permissions: %v", trustee, rights[trustee])
		}
	}
}

// TestServiceInstallRoundTrip registers a throwaway service, checks the
// permissions it gets and removes it again. It needs administrator rights,
// so it runs only where SEVENPANEL_SERVICE_TEST=1 (the Windows CI job).
func TestServiceInstallRoundTrip(t *testing.T) {
	if os.Getenv("SEVENPANEL_SERVICE_TEST") != "1" {
		t.Skip("set SEVENPANEL_SERVICE_TEST=1 to install a test service (needs administrator rights)")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("SEVENPANEL_SERVICE_TEST=1 needs an elevated process")
	}
	name := fmt.Sprintf("7DTDpanelTest%d", time.Now().UnixNano()%1_000_000)
	account := `NT SERVICE\` + name
	paths := ResolvePaths(t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = uninstallServiceAdmin(name, account, paths) })

	if err := installServiceAdmin(name, account, exe, paths); err != nil {
		t.Fatalf("install: %v", err)
	}
	control := windowsServiceControl{name: name, account: account, exe: exe}
	info := control.Info()
	if !info.Installed || !info.CurrentExe || info.AutoStart || info.State != "stopped" {
		t.Fatalf("installed service info = %+v", info)
	}
	m, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Disconnect()
	service, err := m.OpenService(name)
	if err != nil {
		t.Fatal(err)
	}
	config, err := service.Config()
	service.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(config.ServiceStartName, account) || config.StartType != mgr.StartManual {
		t.Fatalf("service config = %+v", config)
	}

	sd, err := windows.GetNamedSecurityInfo(name, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if text := sd.String(); !strings.Contains(text, ";;;IU)") {
		t.Fatalf("service DACL misses interactive users: %s", text)
	}
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		t.Fatal(err)
	}
	folder, err := windows.GetNamedSecurityInfo(paths.Root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if text := folder.String(); !strings.Contains(text, sid.String()) {
		t.Fatalf("panel folder does not grant the service account: %s", text)
	}

	if err := setAutoStartAdmin(name, true); err != nil {
		t.Fatalf("autostart on: %v", err)
	}
	if !control.Info().AutoStart {
		t.Fatal("autostart was not enabled")
	}
	if err := installServiceAdmin(name, account, exe, paths); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if !control.Info().AutoStart {
		t.Fatal("updating the service must keep its start type")
	}

	if err := uninstallServiceAdmin(name, account, paths); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if control.Info().Installed {
		t.Fatal("service still installed")
	}
	folder, err = windows.GetNamedSecurityInfo(paths.Root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(folder.String(), sid.String()) {
		t.Fatalf("uninstall left the folder permission behind: %s", folder)
	}
}

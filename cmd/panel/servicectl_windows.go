package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// windowsServiceControl manages one service registration. name is a field so
// the CI integration test can use a throwaway service name.
type windowsServiceControl struct {
	name, account, exe string
	paths              Paths
	asService          bool
}

func newServiceControl(paths Paths, exe string, asService bool) serviceControl {
	return windowsServiceControl{name: serviceName, account: serviceAccount, exe: exe, paths: paths, asService: asService}
}

// openService opens the service with only the rights asked for, so status
// queries and starting work without administrator rights.
func openService(name string, access uint32) (*mgr.Service, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(scm)
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.OpenService(scm, namePtr, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: name, Handle: handle}, nil
}

func (c windowsServiceControl) Info() ServiceInfo {
	info := ServiceInfo{Supported: true, Name: c.name, Account: c.account, RunningAsService: c.asService}
	service, err := openService(c.name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return info
	}
	if err != nil {
		info.Error = err.Error()
		return info
	}
	defer service.Close()
	info.Installed = true
	if status, err := service.Query(); err == nil {
		info.State = serviceStateName(status.State)
	}
	if config, err := service.Config(); err == nil {
		info.AutoStart = config.StartType == mgr.StartAutomatic
		info.ExePath = serviceExecutable(config.BinaryPathName)
		info.CurrentExe = strings.EqualFold(filepath.Clean(info.ExePath), filepath.Clean(c.exe))
	}
	return info
}

func serviceStateName(state svc.State) string {
	switch state {
	case svc.Running:
		return "running"
	case svc.StartPending:
		return "start_pending"
	case svc.StopPending:
		return "stop_pending"
	default:
		return "stopped"
	}
}

// serviceExecutable extracts the program from a service command line.
func serviceExecutable(commandLine string) string {
	commandLine = strings.TrimSpace(commandLine)
	if rest, ok := strings.CutPrefix(commandLine, `"`); ok {
		if end := strings.Index(rest, `"`); end >= 0 {
			return rest[:end]
		}
		return rest
	}
	if end := strings.IndexByte(commandLine, ' '); end >= 0 {
		return commandLine[:end]
	}
	return commandLine
}

func (c windowsServiceControl) Start() error {
	service, err := openService(c.name, windows.SERVICE_START|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer service.Close()
	if status, err := service.Query(); err == nil && status.State != svc.Stopped {
		return nil
	}
	return service.Start()
}

func (c windowsServiceControl) UninstallSelf() error {
	service, err := openService(c.name, windows.DELETE)
	if err != nil {
		return err
	}
	defer service.Close()
	return service.Delete() // removed by the SCM once this process stops
}

func (c windowsServiceControl) InstallElevated(ctx context.Context) error {
	return runElevated(ctx, c.exe, "service", "install")
}

func (c windowsServiceControl) UninstallElevated(ctx context.Context) error {
	return runElevated(ctx, c.exe, "service", "uninstall")
}

func (c windowsServiceControl) SetAutoStartElevated(ctx context.Context, enabled bool) error {
	return runElevated(ctx, c.exe, "service", "autostart", onOff(enabled))
}

func onOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

// ---------- Administrator operations (run inside the elevated process) ----------

// installServiceAdmin creates or updates the registration so it runs this
// panel.exe under the virtual account, lets interactive users start and stop
// it without a UAC prompt, lets the service delete itself, and grants the
// account Modify rights on the panel folder only.
func installServiceAdmin(name, account, exe string, paths Paths) error {
	if err := preparePanelFolders(paths); err != nil {
		return err
	}
	if err := checkServicePaths(paths); err != nil {
		return fmt.Errorf("the panel folder is not ready: %w", err)
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	config := mgr.Config{
		DisplayName:      "7DTD Server Panel",
		Description:      "Local management panel for a 7 Days to Die dedicated server.",
		StartType:        mgr.StartManual,
		ServiceStartName: account,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}
	service, err := m.OpenService(name)
	if err == nil {
		if err := stopAndWait(service, 30*time.Second); err != nil {
			service.Close()
			return err
		}
		current, err := service.Config()
		if err != nil {
			service.Close()
			return err
		}
		config.BinaryPathName = `"` + exe + `"`
		config.ServiceType = current.ServiceType
		config.ErrorControl = current.ErrorControl
		config.StartType, config.DelayedAutoStart = current.StartType, current.DelayedAutoStart
		err = service.UpdateConfig(config)
		if err != nil {
			service.Close()
			return err
		}
	} else {
		service, err = m.CreateService(name, exe, config)
		if err != nil {
			return err
		}
	}
	defer service.Close()
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		return err
	}
	if err := setServiceSecurity(service.Handle, sid); err != nil {
		return err
	}
	return setFolderAccess(paths.Root, sid, true)
}

func uninstallServiceAdmin(name, account string, paths Paths) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	service, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer service.Close()
	sid, _, _, sidErr := windows.LookupSID("", account)
	if err := stopAndWait(service, 30*time.Second); err != nil {
		return err
	}
	if err := service.Delete(); err != nil {
		return err
	}
	if sidErr == nil {
		return setFolderAccess(paths.Root, sid, false)
	}
	return nil
}

// setAutoStartAdmin switches the service between manual start and delayed
// automatic start, keeping the rest of its configuration.
func setAutoStartAdmin(name string, enabled bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	service, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer service.Close()
	config, err := service.Config()
	if err != nil {
		return err
	}
	config.StartType, config.DelayedAutoStart = mgr.StartManual, false
	if enabled {
		config.StartType, config.DelayedAutoStart = mgr.StartAutomatic, true
	}
	// Config reports the account but never the password; leaving Password
	// empty keeps the virtual account unchanged.
	return service.UpdateConfig(config)
}

func stopAndWait(service *mgr.Service, timeout time.Duration) error {
	status, err := service.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err := service.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop the running service: %w", err)
		}
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if status, err = service.Query(); err != nil || status.State == svc.Stopped {
			return err
		}
	}
	return errors.New("the service did not stop in time")
}

// Service rights in SDDL: CC query config, LC query status, SW enumerate
// dependents, RP start, WP stop, DT pause, LO interrogate, CR user control,
// RC read control, SD delete, DC change config, WD/WO write DAC/owner.
func serviceSDDL(serviceSID string) string {
	return "D:" +
		"(A;;CCLCSWRPWPDTLOCRRC;;;SY)" + // LocalSystem
		"(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)" + // Administrators
		"(A;;CCLCSWRPWPLOCRRC;;;IU)" + // interactive users: query, start, stop
		"(A;;CCLCSWRPWPLOCRRCSD;;;" + serviceSID + ")" // the service itself, incl. deleting itself
}

func setServiceSecurity(handle windows.Handle, sid *windows.SID) error {
	sd, err := windows.SecurityDescriptorFromString(serviceSDDL(sid.String()))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// modifyAccess is the "Modify" permission: read, write, execute and delete.
const modifyAccess = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | windows.DELETE

// setFolderAccess grants (or revokes) the account's inheritable Modify
// permission on the panel folder; Windows propagates it to the contents.
func setFolderAccess(root string, sid *windows.SID, grant bool) error {
	sd, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	current, _, err := sd.DACL()
	if err != nil {
		return err
	}
	mode := windows.ACCESS_MODE(windows.GRANT_ACCESS)
	if !grant {
		mode = windows.REVOKE_ACCESS
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: modifyAccess,
		AccessMode:        mode,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	updated, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, current)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, updated, nil)
}

// ---------- UAC elevation ----------

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

type shellExecuteInfo struct {
	size          uint32
	mask          uint32
	hwnd          windows.Handle
	verb          *uint16
	file          *uint16
	parameters    *uint16
	directory     *uint16
	show          int32
	instApp       windows.Handle
	idList        uintptr
	class         *uint16
	keyClass      windows.Handle
	hotKey        uint32
	iconOrMonitor windows.Handle
	process       windows.Handle
}

const seeMaskNoCloseProcess = 0x40

// runElevated runs this panel.exe with args after a UAC prompt and waits for
// it. The elevated process reports failures through a result file.
func runElevated(ctx context.Context, exe string, args ...string) error {
	result, err := os.CreateTemp("", "7dtd-panel-elevated-*.txt")
	if err != nil {
		return err
	}
	resultPath := result.Name()
	result.Close()
	defer os.Remove(resultPath)
	parameters := windows.ComposeCommandLine(append(args, "--result", resultPath))
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(parameters)
	directory, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	info := shellExecuteInfo{mask: seeMaskNoCloseProcess, verb: verb, file: file, parameters: params, directory: directory, show: windows.SW_HIDE}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return errElevationCanceled
		}
		return callErr
	}
	defer windows.CloseHandle(info.process)
	for {
		event, err := windows.WaitForSingleObject(info.process, 200)
		if err != nil {
			return err
		}
		if event == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return err
	}
	message, _ := os.ReadFile(resultPath)
	if code != 0 {
		if text := strings.TrimSpace(string(message)); text != "" {
			return errors.New(text)
		}
		return fmt.Errorf("the elevated command failed with exit code %d", code)
	}
	return nil
}

// ---------- Command line and double-click behaviour ----------

// runServiceCommand handles "panel.exe service install|uninstall|autostart
// on|off": it elevates itself when needed and writes failures to --result.
func runServiceCommand(args []string, exe string, paths Paths) error {
	var positional []string
	resultPath := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--result" && i+1 < len(args) {
			resultPath = args[i+1]
			i++
			continue
		}
		positional = append(positional, args[i])
	}
	valid := len(positional) == 1 && (positional[0] == "install" || positional[0] == "uninstall")
	valid = valid || (len(positional) == 2 && positional[0] == "autostart" && (positional[1] == "on" || positional[1] == "off"))
	if !valid {
		return errors.New("usage: panel.exe service install|uninstall|autostart on|off")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return runElevated(context.Background(), exe, append([]string{"service"}, positional...)...)
	}
	var err error
	switch positional[0] {
	case "install":
		err = installServiceAdmin(serviceName, serviceAccount, exe, paths)
	case "uninstall":
		err = uninstallServiceAdmin(serviceName, serviceAccount, paths)
	default:
		err = setAutoStartAdmin(serviceName, positional[1] == "on")
	}
	if err != nil && resultPath != "" {
		_ = os.WriteFile(resultPath, []byte(err.Error()), 0o600)
	}
	return err
}

// launchInteractive is what double-clicking panel.exe does. With the service
// installed it makes sure the service runs this panel.exe (updating an
// outdated registration after a UAC prompt), starts it and opens the browser.
// Without the service, or when started as administrator for admin-only tasks
// such as the firewall, it runs the panel in this console window.
func launchInteractive(run func(context.Context) error, exe string, cfg PanelConfig) error {
	control := windowsServiceControl{name: serviceName, account: serviceAccount, exe: exe}
	info := control.Info()
	elevated := windows.GetCurrentProcessToken().IsElevated()
	if elevated && info.Installed && info.State != "" && info.State != "stopped" {
		return fmt.Errorf("面板服务正在运行，占用了面板端口。请先在网页的“面板”页点“停止面板”，再以管理员身份运行 panel.exe")
	}
	if !info.Installed || elevated {
		go openBrowserWhenReady(cfg.Listen)
		return runConsole(run)
	}
	if !info.CurrentExe {
		fmt.Printf("已安装的面板服务指向另一个 panel.exe：%s\n正在请求管理员权限，把服务更新为当前这个 panel.exe…\n", info.ExePath)
		if err := control.InstallElevated(context.Background()); err != nil {
			return fmt.Errorf("更新服务失败: %w", err)
		}
	}
	if err := control.Start(); errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// Installed by an older version without start rights for users.
		fmt.Println("需要管理员权限更新服务权限…")
		if err := control.InstallElevated(context.Background()); err != nil {
			return fmt.Errorf("更新服务失败: %w", err)
		}
		err = control.Start()
		if err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("启动面板服务失败: %w", err)
	}
	if !waitForPanel(cfg.Listen, 30*time.Second) {
		return fmt.Errorf("面板服务已启动，但 30 秒内未能访问 http://%s；请查看 logs\\panel.log", cfg.Listen)
	}
	openBrowser("http://" + cfg.Listen + "/")
	return nil
}

func waitForPanel(listen string, timeout time.Duration) bool {
	client := http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if response, err := client.Get("http://" + listen + "/api/session"); err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return true
			}
		}
	}
	return false
}

func openBrowserWhenReady(listen string) {
	if os.Getenv("SEVENPANEL_NO_BROWSER") != "" {
		return
	}
	if waitForPanel(listen, 30*time.Second) {
		openBrowser("http://" + listen + "/")
	}
}

func openBrowser(url string) {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString(url)
	_ = windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

var procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// waitBeforeExit keeps the console window open after an error when the
// window belongs to this process alone (panel.exe was double-clicked), so
// the message can be read; from an existing terminal it returns at once.
func waitBeforeExit() {
	var ids [4]uint32
	count, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	if count != 1 {
		return
	}
	fmt.Println("按回车键关闭此窗口…")
	_, _ = fmt.Scanln()
}

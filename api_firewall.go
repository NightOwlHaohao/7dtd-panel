package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

const firewallPreviewTTL = time.Minute

type firewallResponse struct {
	Settings FirewallSettings `json:"settings"`
	Desired  []FirewallRule   `json:"desired"`
	Changes  []FirewallChange `json:"changes"`
	CanApply bool             `json:"canApply"`
	Hash     string           `json:"hash,omitempty"`
}

func (a *App) getFirewall(w http.ResponseWriter, req *http.Request) {
	settings, desired, changes, err := a.firewallPlan(req.Context())
	if err != nil {
		a.error(w, http.StatusBadRequest, "firewall_load_failed", sanitizedError(err))
		return
	}
	a.json(w, http.StatusOK, firewallResponse{Settings: settings, Desired: desired, Changes: changes, CanApply: a.canApplyFirewall()})
}

func (a *App) saveFirewallSettings(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Settings *FirewallSettings `json:"settings"`
	}
	if !a.decodeModRequest(w, req, &body) {
		return
	}
	if body.Settings == nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "请求内容无效")
		return
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		a.error(w, http.StatusBadRequest, "config_load_failed", sanitizedError(err))
		return
	}
	if err := validateFirewallManagementCredentials(a.paths.ServerConfig, *body.Settings); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_firewall_settings", "Telnet 对外开放前必须设置密码")
		return
	}
	desired, err := DesiredFirewallRules(doc, *body.Settings)
	if err != nil || firewallUsesPanelPort(desired, a.cfg.Listen) {
		a.error(w, http.StatusBadRequest, "invalid_firewall_settings", "防火墙设置无效或包含面板端口")
		return
	}
	config, err := updatePanelConfig(a.paths.PanelConfig, func(config *PanelConfig) { config.Firewall = *body.Settings })
	if err != nil {
		a.error(w, http.StatusBadRequest, "firewall_settings_failed", sanitizedError(err))
		return
	}
	a.json(w, http.StatusOK, firewallResponse{Settings: config.Firewall, Desired: desired, CanApply: a.canApplyFirewall()})
}

func (a *App) previewFirewall(w http.ResponseWriter, req *http.Request) {
	var body struct{}
	if !a.decodeModRequest(w, req, &body) {
		return
	}
	settings, desired, changes, err := a.firewallPlan(req.Context())
	if err != nil {
		a.error(w, http.StatusBadRequest, "firewall_preview_failed", sanitizedError(err))
		return
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		a.error(w, http.StatusBadRequest, "config_load_failed", sanitizedError(err))
		return
	}
	hash := firewallSettingsHash(doc, settings)
	a.firewallMu.Lock()
	a.firewallHash, a.firewallUntil = hash, time.Now().Add(firewallPreviewTTL)
	a.firewallMu.Unlock()
	a.json(w, http.StatusOK, firewallResponse{Settings: settings, Desired: desired, Changes: changes, CanApply: a.canApplyFirewall(), Hash: hash})
}

func (a *App) applyFirewall(w http.ResponseWriter, req *http.Request) {
	if !a.canApplyFirewall() {
		a.error(w, http.StatusForbidden, "firewall_admin_required", "请以管理员身份运行面板后再应用防火墙规则。")
		return
	}
	var body struct {
		Hash string `json:"hash"`
	}
	if !a.decodeModRequest(w, req, &body) {
		return
	}
	if body.Hash == "" {
		a.error(w, http.StatusBadRequest, "invalid_request", "请求内容无效")
		return
	}
	if !a.firewallPreviewActive(body.Hash) {
		a.error(w, http.StatusConflict, "firewall_stale", "防火墙预览已过期，请重新预览。")
		return
	}
	settings, desired, changes, err := a.firewallPlan(req.Context())
	if err != nil {
		a.error(w, http.StatusBadRequest, "firewall_apply_failed", "无法生成当前防火墙规则。")
		return
	}
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil || firewallSettingsHash(doc, settings) != body.Hash {
		a.error(w, http.StatusConflict, "firewall_stale", "防火墙预览已过期，请重新预览。")
		return
	}
	mutations := make([]FirewallChange, 0, len(changes))
	for _, change := range changes {
		if change.Action != "unchanged" && firewallOwned(change.Rule) {
			mutations = append(mutations, change)
		}
	}
	if len(mutations) > 0 {
		if err := a.firewall.Apply(req.Context(), mutations); err != nil {
			a.console.Append("ops", "error", "firewall", "firewall apply failed: "+sanitizedError(err))
			a.error(w, http.StatusBadRequest, "firewall_apply_failed", "防火墙应用失败。")
			return
		}
	}
	actual, err := a.firewall.List(req.Context())
	if err != nil || !firewallVerified(PreviewFirewall(actual, desired)) {
		a.console.Append("ops", "error", "firewall", "firewall verification failed")
		a.error(w, http.StatusConflict, "firewall_verify_failed", "防火墙规则验证失败。")
		return
	}
	a.console.Append("ops", "info", "firewall", fmt.Sprintf("applied %d owned firewall change(s)", len(mutations)))
	a.json(w, http.StatusOK, firewallResponse{Settings: settings, Desired: desired, Changes: PreviewFirewall(actual, desired), CanApply: true})
}

func (a *App) firewallPlan(ctx context.Context) (FirewallSettings, []FirewallRule, []FirewallChange, error) {
	doc, err := LoadConfig(a.paths.ServerConfig)
	if err != nil {
		return FirewallSettings{}, nil, nil, err
	}
	config, err := LoadPanelConfig(a.paths.PanelConfig)
	if err != nil {
		return FirewallSettings{}, nil, nil, err
	}
	if err := validateFirewallManagementCredentials(a.paths.ServerConfig, config.Firewall); err != nil {
		return FirewallSettings{}, nil, nil, err
	}
	desired, err := DesiredFirewallRules(doc, config.Firewall)
	if err != nil {
		return FirewallSettings{}, nil, nil, err
	}
	if firewallUsesPanelPort(desired, a.cfg.Listen) {
		return FirewallSettings{}, nil, nil, fmt.Errorf("panel port cannot be a firewall rule")
	}
	if a.firewall == nil {
		return FirewallSettings{}, nil, nil, ErrFirewallUnsupported
	}
	actual, err := a.firewall.List(ctx)
	if err != nil {
		return FirewallSettings{}, nil, nil, err
	}
	return config.Firewall, desired, PreviewFirewall(actual, desired), nil
}

func (a *App) canApplyFirewall() bool { return a.firewallAdmin != nil && a.firewallAdmin() }

func validateFirewallManagementCredentials(configPath string, settings FirewallSettings) error {
	if !settings.Telnet {
		return nil
	}
	client, err := telnetFromConfig(configPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(client.Password) == "" {
		return fmt.Errorf("%w: TelnetPassword is required for firewall exposure", ErrFirewallInvalid)
	}
	return nil
}

func firewallCanApply() bool { return exec.Command("net", "session").Run() == nil }

func (a *App) firewallPreviewActive(hash string) bool {
	a.firewallMu.Lock()
	defer a.firewallMu.Unlock()
	return hash != "" && hash == a.firewallHash && time.Now().Before(a.firewallUntil)
}

func firewallSettingsHash(doc ConfigDocument, settings FirewallSettings) string {
	data, _ := json.Marshal(struct {
		Config   string           `json:"config"`
		Settings FirewallSettings `json:"settings"`
	}{doc.Hash, settings})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func firewallVerified(changes []FirewallChange) bool {
	for _, change := range changes {
		if firewallOwned(change.Rule) && change.Action != "unchanged" {
			return false
		}
	}
	return true
}

func firewallUsesPanelPort(rules []FirewallRule, listen string) bool {
	_, portText, err := net.SplitHostPort(listen)
	if err != nil {
		return true
	}
	panelPort, err := firewallPort(portText)
	if err != nil {
		return true
	}
	for _, rule := range rules {
		if firewallRuleUsesPort(rule, panelPort) {
			return true
		}
	}
	return false
}

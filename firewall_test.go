package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func firewallConfig(port string, extra ...ConfigProperty) ConfigDocument {
	return ConfigDocument{Properties: append([]ConfigProperty{{Name: "ServerPort", Value: port}}, extra...)}
}

func firewallRule(id, ports string) FirewallRule {
	return FirewallRule{ID: firewallPrefix + id, Name: firewallPrefix + id, Protocol: "UDP", Ports: ports, Profile: "Any", Enabled: true, Direction: "Inbound", Action: "Allow"}
}

func TestFirewallDefaultsIncludeFullGameUDPRange(t *testing.T) {
	rules, err := DesiredFirewallRules(firewallConfig("26900"), FirewallSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[0].Protocol != "TCP" || rules[0].Ports != "26900" || rules[1].Protocol != "UDP" || rules[1].Ports != "26900-26903" {
		t.Fatalf("rules=%#v", rules)
	}
	for _, port := range []string{"26901", "26902", "26903"} {
		if !strings.Contains(rules[1].Ports, "26900-26903") {
			t.Fatalf("UDP range omitted %s: %#v", port, rules[1])
		}
	}
}

func TestFirewallSettingsSupportIndependentGameRulesAndBothCustomProtocols(t *testing.T) {
	off := false
	rules, err := DesiredFirewallRules(firewallConfig("26900"), FirewallSettings{GameTCP: &off, Custom: []FirewallRule{{Name: "both", Protocol: "Both", Ports: "27000", Profile: "Domain", Enabled: true}}})
	if err != nil || len(rules) != 3 || rules[0].ID == firewallPrefix+"GameTCP" {
		t.Fatalf("rules=%#v err=%v", rules, err)
	}
	for _, rule := range rules {
		if rule.Direction != "Inbound" || rule.Action != "Allow" {
			t.Fatalf("fixed semantics=%#v", rule)
		}
	}
}

func TestFirewallRejectsOverflowAndInvalidCustomPorts(t *testing.T) {
	if _, err := DesiredFirewallRules(firewallConfig("65533"), FirewallSettings{}); err == nil {
		t.Fatal("overflow base port was accepted")
	}
	for _, ports := range []string{"0", "65536", "5-4"} {
		if _, err := DesiredFirewallRules(firewallConfig("26900"), FirewallSettings{Custom: []FirewallRule{{Name: "range", Protocol: "TCP", Ports: ports, Profile: "Private", Enabled: true}}}); err == nil {
			t.Fatalf("custom port %q was accepted", ports)
		}
	}
	rules, err := DesiredFirewallRules(firewallConfig("26900"), FirewallSettings{Custom: []FirewallRule{{Name: "range", Protocol: "TCP", Ports: "1-65535", Profile: "Private", Enabled: true}}})
	if err != nil || len(rules) != 3 || rules[2].Ports != "1-65535" {
		t.Fatalf("rules=%#v err=%v", rules, err)
	}
}

func TestFirewallManagementDefaultsDisabled(t *testing.T) {
	rules, err := DesiredFirewallRules(firewallConfig("26900", ConfigProperty{Name: "WebDashboardEnabled", Value: "true"}, ConfigProperty{Name: "WebDashboardPort", Value: "8080"}, ConfigProperty{Name: "TelnetEnabled", Value: "true"}, ConfigProperty{Name: "TelnetPort", Value: "8081"}), FirewallSettings{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if rule.Management {
			t.Fatalf("default management rule=%#v", rule)
		}
	}
}

func TestFirewallRejectsCustomTCPOverlapWithEnabledManagementPorts(t *testing.T) {
	doc := firewallConfig("26900",
		ConfigProperty{Name: "WebDashboardEnabled", Value: "true"}, ConfigProperty{Name: "WebDashboardPort", Value: "8080"},
		ConfigProperty{Name: "TelnetEnabled", Value: "true"}, ConfigProperty{Name: "TelnetPort", Value: "8081"},
	)
	for _, protocol := range []string{"TCP", "Both"} {
		_, err := DesiredFirewallRules(doc, FirewallSettings{Custom: []FirewallRule{{Name: "wide", Protocol: protocol, Ports: "8000-9000", Profile: "Private", Enabled: true}}})
		if !errors.Is(err, ErrFirewallInvalid) {
			t.Fatalf("%s management overlap error=%v", protocol, err)
		}
	}
	if _, err := DesiredFirewallRules(doc, FirewallSettings{Custom: []FirewallRule{{Name: "udp", Protocol: "UDP", Ports: "8000-9000", Profile: "Private", Enabled: true}}}); err != nil {
		t.Fatalf("UDP does not expose TCP management listeners: %v", err)
	}
}

func TestFirewallTelnetExposureRequiresPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serverconfig.xml")
	write := func(password string) {
		xml := `<ServerSettings><property name="TelnetEnabled" value="true"/><property name="TelnetPort" value="8081"/><property name="TelnetPassword" value="` + password + `"/></ServerSettings>`
		if err := os.WriteFile(path, []byte(xml), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("")
	if err := validateFirewallManagementCredentials(path, FirewallSettings{Telnet: true}); !errors.Is(err, ErrFirewallInvalid) {
		t.Fatalf("passwordless exposure error=%v", err)
	}
	write("configured")
	if err := validateFirewallManagementCredentials(path, FirewallSettings{Telnet: true}); err != nil {
		t.Fatalf("password-protected exposure error=%v", err)
	}
}

func TestFirewallListRoundTripsFixedManagementIdentity(t *testing.T) {
	doc := firewallConfig("26900", ConfigProperty{Name: "WebDashboardEnabled", Value: "true"}, ConfigProperty{Name: "WebDashboardPort", Value: "8080"})
	desired, err := DesiredFirewallRules(doc, FirewallSettings{Dashboard: true, Custom: []FirewallRule{{Name: "range", Protocol: "TCP", Ports: "9000", Profile: "Private", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	listed := append([]FirewallRule(nil), desired...)
	for i := range listed {
		listed[i].Management = false // Windows exposes no user-controlled management field.
	}
	payload, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	backend := WindowsFirewallBackend{Run: func(context.Context, []string, []byte) ([]byte, error) { return payload, nil }}
	actual, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range actual {
		if rule.ID == firewallPrefix+"Dashboard" && !rule.Management {
			t.Fatalf("dashboard was not reconstructed: %#v", rule)
		}
		if (rule.ID == firewallPrefix+"GameTCP" || strings.HasPrefix(rule.ID, firewallPrefix+"Custom-")) && rule.Management {
			t.Fatalf("non-management rule changed: %#v", rule)
		}
	}
	for _, change := range PreviewFirewall(actual, desired) {
		if change.Action != "unchanged" {
			t.Fatalf("roundtrip change=%#v", change)
		}
	}
}

func TestFirewallPreviewIsStableAndIsolatesThirdPartyRules(t *testing.T) {
	tcp := firewallRule("GameTCP", "26900")
	tcp.Protocol = "TCP"
	udp := firewallRule("GameUDP", "26900-26903")
	custom := firewallRule("Custom-abc", "1-65535")
	actual := []FirewallRule{tcp, firewallRule("GameUDP", "26900-26902"), firewallRule("Stale", "1"), {ID: "third-party", Name: "third-party", Protocol: "TCP", Ports: "80", Profile: "Any", Enabled: true, Direction: "Inbound", Action: "Allow"}}
	changes := PreviewFirewall(actual, []FirewallRule{tcp, udp, custom})
	got := map[string]string{}
	for _, change := range changes {
		got[change.Rule.ID] = change.Action
		if change.Rule.ID == "third-party" && change.Action != "unchanged" {
			t.Fatalf("third-party change=%#v", change)
		}
	}
	for id, action := range map[string]string{tcp.ID: "unchanged", udp.ID: "modify", custom.ID: "add", firewallPrefix + "Stale": "remove", "third-party": "unchanged"} {
		if got[id] != action {
			t.Fatalf("changes=%#v want %s=%s", changes, id, action)
		}
	}
	if again := PreviewFirewall(actual, []FirewallRule{tcp, udp, custom}); len(again) != len(changes) {
		t.Fatalf("preview is not stable: %#v", again)
	}
}

func TestFirewallApplyRejectsUnownedAndKeepsInputOutOfPowerShell(t *testing.T) {
	calls := 0
	backend := WindowsFirewallBackend{Run: func(_ context.Context, args []string, input []byte) ([]byte, error) {
		calls++
		if strings.Contains(strings.Join(args, " "), "injected") {
			t.Fatalf("untrusted text reached PowerShell arguments: %q", args)
		}
		if !strings.Contains(string(input), "injected") {
			t.Fatalf("expected JSON stdin, got %q", input)
		}
		return nil, nil
	}}
	thirdParty := FirewallChange{Action: "add", Rule: FirewallRule{ID: "other", Name: "other", Protocol: "TCP", Ports: "80", Profile: "Any", Enabled: true}}
	if err := backend.Apply(context.Background(), []FirewallChange{thirdParty}); !errors.Is(err, ErrFirewallOwnership) || calls != 0 {
		t.Fatalf("third party Apply() err=%v calls=%d", err, calls)
	}
	malicious := FirewallChange{Action: "add", Rule: FirewallRule{ID: firewallPrefix + "Good", Name: firewallPrefix + "Good; injected", Protocol: "TCP", Ports: "80", Profile: "Any", Enabled: true}}
	if err := backend.Apply(context.Background(), []FirewallChange{malicious}); !errors.Is(err, ErrFirewallInvalid) || calls != 0 {
		t.Fatalf("malicious Apply() err=%v calls=%d", err, calls)
	}
	safe := firewallRule("Good", "80")
	safe.Protocol = "TCP"
	safe.Note = "injected; still stdin data"
	if err := backend.Apply(context.Background(), []FirewallChange{{Action: "add", Rule: safe}}); err != nil || calls != 1 {
		t.Fatalf("safe Apply() err=%v calls=%d", err, calls)
	}
}

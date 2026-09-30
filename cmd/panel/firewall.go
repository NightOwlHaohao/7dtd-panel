package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const firewallPrefix = "7DTDserverPanel-"

var (
	ErrFirewallUnsupported = errors.New("firewall is unsupported on this platform")
	ErrFirewallOwnership   = errors.New("firewall rule is not panel-owned")
	ErrFirewallInvalid     = errors.New("firewall rule is invalid")
	firewallName           = regexp.MustCompile(`^7DTDserverPanel-[A-Za-z0-9._-]{1,100}$`)
)

type FirewallRule struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	Ports      string `json:"ports"`
	Profile    string `json:"profile"`
	Note       string `json:"note"`
	Enabled    bool   `json:"enabled"`
	Management bool   `json:"management"`
	Direction  string `json:"direction"`
	Action     string `json:"action"`
}

type FirewallChange struct {
	Action string       `json:"action"`
	Rule   FirewallRule `json:"rule"`
}

type FirewallBackend interface {
	List(context.Context) ([]FirewallRule, error)
	Apply(context.Context, []FirewallChange) error
}

type FirewallSettings struct {
	GameTCP   *bool          `json:"gameTCP,omitempty"`
	GameUDP   *bool          `json:"gameUDP,omitempty"`
	Dashboard bool           `json:"dashboard"`
	Telnet    bool           `json:"telnet"`
	Custom    []FirewallRule `json:"custom"`
}

type WindowsFirewallBackend struct {
	Run func(context.Context, []string, []byte) ([]byte, error)
}

func DesiredFirewallRules(doc ConfigDocument, settings FirewallSettings) ([]FirewallRule, error) {
	base, err := firewallPort(configString(doc, "ServerPort"))
	if err != nil || base > 65532 {
		return nil, fmt.Errorf("%w: ServerPort", ErrFirewallInvalid)
	}
	rules := []FirewallRule{}
	if settings.GameTCP == nil || *settings.GameTCP {
		rules = append(rules, firewallManaged("GameTCP", "TCP", strconv.Itoa(base), "Any", "Game server TCP", false))
	}
	if settings.GameUDP == nil || *settings.GameUDP {
		rules = append(rules, firewallManaged("GameUDP", "UDP", fmt.Sprintf("%d-%d", base, base+3), "Any", "Game server UDP", false))
	}
	if settings.Dashboard {
		port, err := firewallManagementPort(doc, "WebDashboardEnabled", "WebDashboardPort")
		if err != nil {
			return nil, err
		}
		rules = append(rules, firewallManaged("Dashboard", "TCP", strconv.Itoa(port), "Any", "Web dashboard management", true))
	}
	if settings.Telnet {
		port, err := firewallManagementPort(doc, "TelnetEnabled", "TelnetPort")
		if err != nil {
			return nil, err
		}
		rules = append(rules, firewallManaged("Telnet", "TCP", strconv.Itoa(port), "Any", "Telnet management", true))
	}
	custom := make([]FirewallRule, 0, len(settings.Custom))
	for _, rule := range settings.Custom {
		protocols := []string{rule.Protocol}
		if strings.EqualFold(strings.TrimSpace(rule.Protocol), "Both") {
			protocols = []string{"TCP", "UDP"}
		}
		for _, protocol := range protocols {
			copy := rule
			copy.Protocol = protocol
			copy, err = firewallCustom(copy)
			if err != nil {
				return nil, err
			}
			if copy.Enabled && copy.Protocol == "TCP" {
				for _, port := range firewallEnabledManagementPorts(doc) {
					if firewallRuleUsesPort(copy, port) {
						return nil, fmt.Errorf("%w: custom rule overlaps management port %d", ErrFirewallInvalid, port)
					}
				}
			}
			custom = append(custom, copy)
		}
	}
	sort.Slice(custom, func(i, j int) bool { return custom[i].ID < custom[j].ID })
	return append(rules, custom...), nil
}

func PreviewFirewall(actual, desired []FirewallRule) []FirewallChange {
	actualOwned := map[string]FirewallRule{}
	changes := make([]FirewallChange, 0, len(actual)+len(desired))
	for _, rule := range actual {
		if firewallOwned(rule) {
			actualOwned[rule.ID] = rule
		} else {
			changes = append(changes, FirewallChange{Action: "unchanged", Rule: rule})
		}
	}
	desired = append([]FirewallRule(nil), desired...)
	sort.Slice(desired, func(i, j int) bool { return desired[i].ID < desired[j].ID })
	for _, rule := range desired {
		if !firewallOwned(rule) {
			changes = append(changes, FirewallChange{Action: "unchanged", Rule: rule})
			continue
		}
		actual, ok := actualOwned[rule.ID]
		delete(actualOwned, rule.ID)
		if !ok {
			changes = append(changes, FirewallChange{Action: "add", Rule: rule})
		} else if actual == rule {
			changes = append(changes, FirewallChange{Action: "unchanged", Rule: rule})
		} else {
			changes = append(changes, FirewallChange{Action: "modify", Rule: rule})
		}
	}
	for _, rule := range actualOwned {
		changes = append(changes, FirewallChange{Action: "remove", Rule: rule})
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Rule.ID != changes[j].Rule.ID {
			return changes[i].Rule.ID < changes[j].Rule.ID
		}
		return changes[i].Action < changes[j].Action
	})
	return changes
}

func (b WindowsFirewallBackend) List(ctx context.Context) ([]FirewallRule, error) {
	output, err := b.run(ctx, firewallListScript, nil)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil, nil
	}
	var rules []FirewallRule
	if err := json.Unmarshal(output, &rules); err != nil {
		return nil, err
	}
	for i := range rules {
		rules[i].Management = firewallManagementRule(rules[i])
	}
	return rules, nil
}

func (b WindowsFirewallBackend) Apply(ctx context.Context, changes []FirewallChange) error {
	mutations := make([]FirewallChange, 0, len(changes))
	for _, change := range changes {
		if err := validateFirewallChange(change); err != nil {
			return err
		}
		if change.Action != "unchanged" {
			mutations = append(mutations, change)
		}
	}
	if len(mutations) == 0 {
		return nil
	}
	input, err := json.Marshal(mutations)
	if err != nil {
		return err
	}
	_, err = b.run(ctx, firewallApplyScript, input)
	return err
}

func (b WindowsFirewallBackend) run(ctx context.Context, script string, input []byte) ([]byte, error) {
	if b.Run != nil {
		return b.Run(ctx, firewallPowerShellArgs(script), input)
	}
	return runFirewallPowerShell(ctx, firewallPowerShellArgs(script), input)
}

func firewallPowerShellArgs(script string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-Command", script}
}

func firewallManaged(id, protocol, ports, profile, note string, management bool) FirewallRule {
	name := firewallPrefix + id
	return FirewallRule{ID: name, Name: name, Protocol: protocol, Ports: ports, Profile: profile, Note: note, Enabled: true, Management: management, Direction: "Inbound", Action: "Allow"}
}

func firewallCustom(rule FirewallRule) (FirewallRule, error) {
	protocol, err := firewallProtocol(rule.Protocol)
	if err != nil {
		return FirewallRule{}, err
	}
	ports, err := firewallPorts(rule.Ports)
	if err != nil {
		return FirewallRule{}, err
	}
	profile, err := firewallProfile(rule.Profile)
	if err != nil {
		return FirewallRule{}, err
	}
	label := strings.TrimSpace(rule.Name)
	if label == "" {
		return FirewallRule{}, fmt.Errorf("%w: custom name", ErrFirewallInvalid)
	}
	hash := sha256.Sum256([]byte(strings.Join([]string{label, protocol, ports, profile}, "\x00")))
	id := firewallPrefix + "Custom-" + hex.EncodeToString(hash[:8])
	return FirewallRule{ID: id, Name: id, Protocol: protocol, Ports: ports, Profile: profile, Note: strings.TrimSpace(rule.Note), Enabled: rule.Enabled, Management: false, Direction: "Inbound", Action: "Allow"}, nil
}

func firewallManagementPort(doc ConfigDocument, enabledName, portName string) (int, error) {
	enabled, err := strconv.ParseBool(configString(doc, enabledName))
	if err != nil || !enabled {
		return 0, fmt.Errorf("%w: %s is unavailable", ErrFirewallInvalid, enabledName)
	}
	port, err := firewallPort(configString(doc, portName))
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrFirewallInvalid, portName)
	}
	return port, nil
}

func firewallEnabledManagementPorts(doc ConfigDocument) []int {
	ports := make([]int, 0, 2)
	for _, names := range [][2]string{{"WebDashboardEnabled", "WebDashboardPort"}, {"TelnetEnabled", "TelnetPort"}} {
		if port, err := firewallManagementPort(doc, names[0], names[1]); err == nil {
			ports = append(ports, port)
		}
	}
	return ports
}

func firewallRuleUsesPort(rule FirewallRule, port int) bool {
	ports, err := firewallPorts(rule.Ports)
	if err != nil {
		return false
	}
	parts := strings.Split(ports, "-")
	first, _ := strconv.Atoi(parts[0])
	last := first
	if len(parts) == 2 {
		last, _ = strconv.Atoi(parts[1])
	}
	return first <= port && port <= last
}

func validateFirewallChange(change FirewallChange) error {
	if change.Action != "add" && change.Action != "modify" && change.Action != "remove" && change.Action != "unchanged" {
		return fmt.Errorf("%w: action", ErrFirewallInvalid)
	}
	if !firewallOwned(change.Rule) {
		return ErrFirewallOwnership
	}
	return validateFirewallRule(change.Rule)
}

func validateFirewallRule(rule FirewallRule) error {
	if !firewallOwned(rule) {
		return ErrFirewallOwnership
	}
	if !firewallName.MatchString(rule.ID) || !firewallName.MatchString(rule.Name) {
		return ErrFirewallInvalid
	}
	if _, err := firewallProtocol(rule.Protocol); err != nil {
		return err
	}
	if _, err := firewallPorts(rule.Ports); err != nil {
		return err
	}
	if _, err := firewallProfile(rule.Profile); err != nil {
		return err
	}
	if rule.Direction != "Inbound" || rule.Action != "Allow" {
		return fmt.Errorf("%w: direction/action", ErrFirewallInvalid)
	}
	return nil
}

func firewallOwned(rule FirewallRule) bool {
	return strings.HasPrefix(rule.ID, firewallPrefix) && strings.HasPrefix(rule.Name, firewallPrefix)
}

func firewallManagementRule(rule FirewallRule) bool {
	return rule.ID == rule.Name && (rule.ID == firewallPrefix+"Dashboard" || rule.ID == firewallPrefix+"Telnet")
}

func firewallProtocol(protocol string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "TCP", "UDP":
		return strings.ToUpper(strings.TrimSpace(protocol)), nil
	default:
		return "", fmt.Errorf("%w: protocol", ErrFirewallInvalid)
	}
}

func firewallProfile(profile string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "any":
		return "Any", nil
	case "private":
		return "Private", nil
	case "public":
		return "Public", nil
	case "domain":
		return "Domain", nil
	default:
		return "", fmt.Errorf("%w: profile", ErrFirewallInvalid)
	}
}

func firewallPorts(value string) (string, error) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) < 1 || len(parts) > 2 {
		return "", fmt.Errorf("%w: ports", ErrFirewallInvalid)
	}
	first, err := firewallPort(parts[0])
	if err != nil {
		return "", err
	}
	if len(parts) == 1 {
		return strconv.Itoa(first), nil
	}
	last, err := firewallPort(parts[1])
	if err != nil || first > last {
		return "", fmt.Errorf("%w: ports", ErrFirewallInvalid)
	}
	return fmt.Sprintf("%d-%d", first, last), nil
}

func firewallPort(value string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%w: port", ErrFirewallInvalid)
	}
	return port, nil
}

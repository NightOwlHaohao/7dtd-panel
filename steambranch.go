package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const gameAppID = "294420"

// SteamSettings choose which Steam branch SteamCMD installs. An empty
// Branch passes no -beta option, as earlier versions did.
type SteamSettings struct {
	Branch         string `json:"branch,omitempty"`
	BranchPassword string `json:"branchPassword,omitempty"`
}

var (
	ErrInvalidBranch = errors.New("invalid Steam branch")
	branchName       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

func validateSteamSettings(settings SteamSettings) error {
	if settings.Branch != "" && !branchName.MatchString(settings.Branch) {
		return fmt.Errorf("%w: %q", ErrInvalidBranch, settings.Branch)
	}
	if settings.Branch == "" && settings.BranchPassword != "" {
		return fmt.Errorf("%w: a password needs a branch", ErrInvalidBranch)
	}
	if len(settings.BranchPassword) > 128 || strings.ContainsFunc(settings.BranchPassword, func(r rune) bool { return r < 0x21 || r == 0x7f }) {
		return fmt.Errorf("%w: the password must be printable without spaces", ErrInvalidBranch)
	}
	return nil
}

// appUpdateArgs are the SteamCMD arguments that install or update the game.
func appUpdateArgs(serverDir string, settings SteamSettings) []string {
	args := []string{"+force_install_dir", serverDir, "+login", "anonymous", "+app_update", gameAppID}
	if settings.Branch != "" {
		args = append(args, "-beta", settings.Branch)
		if settings.BranchPassword != "" {
			args = append(args, "-betapassword", settings.BranchPassword)
		}
	}
	return append(args, "validate", "+quit")
}

// InstalledGame is what Steam recorded about the installed server.
type InstalledGame struct {
	Installed bool   `json:"installed"`
	BuildID   string `json:"buildId,omitempty"`
	Branch    string `json:"branch,omitempty"` // "public" unless a beta is installed
}

// readInstalledGame reads server\steamapps\appmanifest_294420.acf.
func readInstalledGame(serverDir string) InstalledGame {
	data, err := os.ReadFile(filepath.Join(serverDir, "steamapps", "appmanifest_"+gameAppID+".acf"))
	if err != nil {
		return InstalledGame{}
	}
	root, err := parseVDF(string(data))
	if err != nil {
		return InstalledGame{}
	}
	state := vdfMap(root, "AppState")
	game := InstalledGame{Installed: true, BuildID: vdfString(state, "buildid"), Branch: "public"}
	for _, section := range []string{"UserConfig", "MountedConfig"} {
		if beta := vdfString(vdfMap(state, section), "BetaKey"); beta != "" {
			game.Branch = beta
			break
		}
	}
	return game
}

// RemoteBranch is one branch from SteamCMD's app_info_print.
type RemoteBranch struct {
	Name        string `json:"name"`
	BuildID     string `json:"buildId"`
	Updated     int64  `json:"updated,omitempty"` // Unix seconds
	Password    bool   `json:"password,omitempty"`
	Description string `json:"description,omitempty"`
}

// parseAppInfoBranches extracts the branches from app_info_print output,
// which prints other text around the "294420" { … } block.
func parseAppInfoBranches(output string) ([]RemoteBranch, error) {
	start := strings.Index(output, `"`+gameAppID+`"`)
	if start < 0 {
		return nil, errors.New("SteamCMD printed no app info for " + gameAppID)
	}
	root, err := parseVDF(output[start:])
	if err != nil {
		return nil, err
	}
	branches := vdfMap(vdfMap(vdfMap(root, gameAppID), "depots"), "branches")
	if branches == nil {
		return nil, errors.New("SteamCMD app info has no branches")
	}
	var out []RemoteBranch
	for _, name := range vdfKeys(branches) {
		branch := vdfMap(branches, name)
		if branch == nil {
			continue
		}
		updated, _ := strconv.ParseInt(vdfString(branch, "timeupdated"), 10, 64)
		out = append(out, RemoteBranch{Name: name, BuildID: vdfString(branch, "buildid"), Updated: updated, Password: vdfString(branch, "pwdrequired") == "1", Description: vdfString(branch, "description")})
	}
	return out, nil
}

// ---------- A small Valve KeyValues (VDF) reader ----------

// vdfNode keeps keys in file order; values are strings or *vdfNode.
type vdfNode struct {
	keys   []string
	values map[string]any
}

func vdfMap(node *vdfNode, key string) *vdfNode {
	if node == nil {
		return nil
	}
	child, _ := node.lookup(key).(*vdfNode)
	return child
}

func vdfString(node *vdfNode, key string) string {
	if node == nil {
		return ""
	}
	value, _ := node.lookup(key).(string)
	return value
}

func vdfKeys(node *vdfNode) []string { return append([]string(nil), node.keys...) }

// lookup is case-insensitive, like Steam's own reader.
func (n *vdfNode) lookup(key string) any {
	if value, ok := n.values[key]; ok {
		return value
	}
	for _, name := range n.keys {
		if strings.EqualFold(name, key) {
			return n.values[name]
		}
	}
	return nil
}

func (n *vdfNode) set(key string, value any) {
	if _, ok := n.values[key]; !ok {
		n.keys = append(n.keys, key)
	}
	n.values[key] = value
}

// parseVDF reads key/value pairs; parsing stops without error when the
// top-level block ends, so trailing text is ignored.
func parseVDF(text string) (*vdfNode, error) {
	tokens, err := vdfTokens(text)
	if err != nil {
		return nil, err
	}
	pos := 0
	root := &vdfNode{values: map[string]any{}}
	var parse func(node *vdfNode, depth int) error
	parse = func(node *vdfNode, depth int) error {
		if depth > 32 {
			return errors.New("VDF nests too deeply")
		}
		for pos < len(tokens) {
			key := tokens[pos]
			if key == "}" {
				if depth == 0 {
					return errors.New("unexpected } in VDF")
				}
				pos++
				return nil
			}
			if key == "{" {
				return errors.New("unexpected { in VDF")
			}
			pos++
			if pos >= len(tokens) {
				return errors.New("VDF key without value")
			}
			if tokens[pos] == "{" {
				pos++
				child := &vdfNode{values: map[string]any{}}
				if err := parse(child, depth+1); err != nil {
					return err
				}
				node.set(unquoteVDF(key), child)
				if depth == 0 {
					return nil // one top-level block
				}
				continue
			}
			if tokens[pos] == "}" {
				return errors.New("VDF key without value")
			}
			node.set(unquoteVDF(key), unquoteVDF(tokens[pos]))
			pos++
		}
		if depth > 0 {
			return errors.New("unterminated VDF block")
		}
		return nil
	}
	if err := parse(root, 0); err != nil {
		return nil, err
	}
	return root, nil
}

// vdfTokens splits quoted strings (kept with a leading quote), braces and
// bare words; // comments are skipped.
func vdfTokens(text string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == '{' || c == '}':
			tokens = append(tokens, string(c))
			i++
		case c == '"':
			var b strings.Builder
			b.WriteByte('"')
			i++
			closed := false
			for i < len(text) {
				if text[i] == '\\' && i+1 < len(text) {
					switch text[i+1] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(text[i+1])
					}
					i += 2
					continue
				}
				if text[i] == '"' {
					closed = true
					i++
					break
				}
				b.WriteByte(text[i])
				i++
			}
			if !closed {
				return nil, errors.New("unterminated string in VDF")
			}
			tokens = append(tokens, b.String())
		default:
			start := i
			for i < len(text) && !strings.ContainsRune(" \t\r\n{}\"", rune(text[i])) {
				i++
			}
			tokens = append(tokens, text[start:i])
		}
	}
	return tokens, nil
}

func unquoteVDF(token string) string { return strings.TrimPrefix(token, `"`) }

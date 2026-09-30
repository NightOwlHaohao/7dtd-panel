//go:build !windows

package main

import "context"

const firewallListScript = ""
const firewallApplyScript = ""

func runFirewallPowerShell(context.Context, []string, []byte) ([]byte, error) {
	return nil, ErrFirewallUnsupported
}

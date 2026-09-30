//go:build windows

package main

import (
	"bytes"
	"context"
	"os/exec"
)

const firewallListScript = `$ErrorActionPreference = 'Stop'
@((Get-NetFirewallRule -DisplayName '7DTDserverPanel-*') | ForEach-Object {
  $filter = $_ | Get-NetFirewallPortFilter
  [PSCustomObject]@{ ID = $_.Name; Name = $_.DisplayName; Protocol = [string]$filter.Protocol; Ports = [string]$filter.LocalPort; Profile = [string]$_.Profile; Note = [string]$_.Description; Enabled = ([string]$_.Enabled -eq 'True'); Management = $false; Direction = [string]$_.Direction; Action = [string]$_.Action }
}) | ConvertTo-Json -Compress`

const firewallApplyScript = `$ErrorActionPreference = 'Stop'
$changes = [Console]::In.ReadToEnd() | ConvertFrom-Json
foreach ($change in @($changes)) {
  $rule = $change.Rule
  $enabled = if ([bool]$rule.Enabled) { 'True' } else { 'False' }
  switch ($change.Action) {
    'add' { New-NetFirewallRule -Name $rule.ID -DisplayName $rule.Name -Direction Inbound -Action Allow -Protocol $rule.Protocol -LocalPort $rule.Ports -Profile $rule.Profile -Description $rule.Note -Enabled $enabled | Out-Null }
    'modify' { Set-NetFirewallRule -Name $rule.ID -DisplayName $rule.Name -Direction $rule.Direction -Action $rule.Action -Profile $rule.Profile -Description $rule.Note -Enabled $enabled | Out-Null; Set-NetFirewallPortFilter -AssociatedNetFirewallRule (Get-NetFirewallRule -Name $rule.ID) -Protocol $rule.Protocol -LocalPort $rule.Ports | Out-Null }
    'remove' { Remove-NetFirewallRule -Name $rule.ID }
  }
}`

func runFirewallPowerShell(ctx context.Context, args []string, input []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}

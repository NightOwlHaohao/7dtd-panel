# Roadmap

[简体中文](ROADMAP.md) | English

Last updated: 2026-09-30 (v1.1.0). Read [`CLAUDE.md`](../CLAUDE.md) before contributing. All backend code is in `cmd/panel/`.

## Done (v1.1.0)

### Windows service managed by panel.exe

`scripts\` is gone; `panel.exe` manages its own service (`servicectl.go`, `servicectl_windows.go`, `servicectl_other.go`):

- Double-click (`launchInteractive`): without the service, or when run as administrator → console mode
  and open the browser; with the service → update its path after a UAC prompt if needed → start it →
  wait for the port → open the browser. Run as administrator while the service holds the port, it
  explains and exits; on errors the console window waits for Enter before closing.
- Command line: `panel.exe service install|uninstall|autostart on|off` (elevating itself through UAC and
  reporting back through a `--result` file) and `panel.exe --console`.
- API: `GET /api/panel`, `POST /api/panel/stop`, `POST /api/panel/service/install|uninstall`,
  `PUT /api/panel/service/autostart`; the Panel page in the UI.
- Service account `NT SERVICE\7DTDserverPanel`; DACL: full control for SY/BA, query/start/stop for
  interactive users (IU), and DELETE for the service's own SID. **Only administrators may change the
  service configuration**: granting DC to the service SID or IU would let the service reconfigure itself
  as LocalSystem, a privilege escalation. So a panel running as the service cannot switch autostart from
  the web page; use `panel.exe service autostart on`.
- The service preflight does not require the game files; updating the service keeps its start type; the
  release is named `7dtd-panel-<version>-windows-x64.zip`.
- Windows CI sets `SEVENPANEL_SERVICE_TEST=1` to install a throwaway service, check its DACL, the folder
  ACL and autostart, and remove it.

### Features

1. **Backup restore** (`backup.go`, `backup_restore.go`): one-click restore (the current userdata moves to
   `backups\before-restore`, newest 3 kept, rolled back on failure), deleting backups, hot backups while
   the game runs (after `saveworld`).
2. **Schedule** (`scheduler.go`, `api_schedule.go`): restarts with in-game `say` countdowns, automatic
   backups (`userdata-auto-*`, pruned to a count), console commands and announcements; restart after a
   crash (at most 3 times in 30 minutes); start the game when the panel starts.
3. **SteamCMD branch** (`steambranch.go`): `steam.branch/branchPassword` in `panel.json`, with a small VDF reader.
4. **Players** (`players.go`, `api_players.go`): online list from `lp`; kick, ban, whitelist, admin,
   private messages and announcements; `serveradmin.xml` lists; players seen (`cache\players.json`).
5. **NaiwaziBot companion** (`naiwazi.go`): detects NaiwaziBot in Mods, works out its web port (game port
   + 6, or set by hand), checks it is listening, opens it and shows its login file. It is free but not open
   source (its GitHub repository holds only a README; the release is obfuscated .NET without a licence),
   so **never put its files in this repository**; its ZIP installs through the Mods page.
6. **Autostart**: delayed automatic start of the service plus "Start the game when the panel starts".
7. **Update reminders** (`versioncheck.go`, `api_versions.go`): GitHub Release daily, SteamCMD
   `app_info_print` every six hours (never during an update); each new version announced once; switchable.

### Preparing the public repository

Licence changed to GNU AGPL-3.0; build outputs and old AI planning
notes removed; Go code moved to `cmd/panel/`; documentation split into README / user guide / development
in Chinese and English; SECURITY.md and issue templates added.

Still to confirm on a real machine: the UAC flows (install and switch, updating an old service path on
double-click), starting the game after a delayed automatic boot start, and the real output of
`lp`/`ban`/`sayplayer`.

## Planned (by priority)

1. **Discord / webhook notifications**: server up/down, crashes, players joining/leaving, failed tasks, updates.
2. **Several server instances**: one panel managing several server folders and ports.
3. **More player management**: offline players from the save, parsing Telnet `ban list`/`admin list`
   (without the file), templates for teleport/give commands.
4. **Better schedule**: persistent run history (in memory today), retries, skipping restarts by player count.
5. **Backups to another disk or network location**, backup size and disk space warnings.
6. **Split the code**: divide `cmd/panel` into Go packages by area (server process, Telnet, mods, saves …).

## Known limitations

- GPU usage cannot be verified in CI (no GPU); check it on a real machine.
- As a service, "Open backup folder" cannot open an Explorer window and shows the path instead.
- Hot backups copy files while the game writes them; rarely a file may be inconsistent — stop the server
  for important backups.
- Player command formats follow V1.x/V2.x (`pltfmid=`, `<users>`) and accept A20's `steamID`; a new game
  version that changes them needs parser updates.
- Scheduled times use the panel computer's local time zone; runs missed by more than 5 minutes are skipped.

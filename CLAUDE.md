# 7DTD Panel — project memory

Local web panel for running a **7 Days to Die dedicated server on Windows**. One Go
program (`panel.exe`) with the web UI embedded; listens on `127.0.0.1` only.
Author: NightowlHaohao. License: **PolyForm Noncommercial 1.0.0** (no commercial use; keep the
`Required Notice:` line in LICENSE). The owner writes in Chinese — reply in Chinese.

Current release: **v1.1.0** (GitHub Release, built by `.github/workflows/release.yml`).
Open work and ideas: **`docs/ROADMAP.md`** (read it before starting new work). User docs:
`README.md` (short) and `docs/使用说明.md` (per page); developer docs: `docs/开发说明.md`.
The repository is public: never commit personal paths, e-mail addresses, build outputs or
third-party binaries (e.g. NaiwaziBot).

## Commands

```sh
go vet ./... && GOOS=windows go vet ./...   # both platforms must vet clean
go test -race -count=1 ./...                # Linux; Windows-only tests skip
cd web && node --test                       # frontend tests (node 22, no deps)
gofmt -l .                                  # must print nothing (CI checks)
```

Windows release build: `build.ps1` (runs tests, stamps `main.version` from
`git describe --tags`, writes `dist\release`). CI (`.github/workflows/ci.yml`)
runs vet/tests on Linux (+race) and Windows, runs `build.ps1` under Windows
PowerShell 5.1 and uploads `dist\release` as an artifact.

Release: Actions → Release → Run workflow with version `vX.Y.Z` (or push a
`v*` tag). It tests, builds, zips and creates the GitHub Release.
**Cloud Claude sessions cannot push tags or push to `main`** (git proxy allows
only the session branch) — use PRs, then trigger the Release workflow via the
GitHub MCP `actions_run_trigger` tool.

## Layout

| Area | Files |
|---|---|
| Entry, service mode | `main.go` (subcommands `service …`, `--console`), `service_windows.go` (`svc.Run`), `service_other.go` |
| Service management | `servicectl.go` (API, lifecycle), `servicectl_windows.go` (SCM, DACL, UAC via ShellExecuteEx runas, double-click launcher), `servicectl_other.go` |
| HTTP wiring, security, static files | `http.go` (Host/Origin/token checks, CSP, `go:embed` of `web/index.html web/favicon.svg web/css web/js`) |
| API handlers by area | `api_server.go`, `api_config.go` (config + sandbox), `api_mods.go`, `api_saves.go` (saves + backups), `api_firewall.go` |
| Game process lifecycle | `server.go` (state machine: stopped/starting/running/stopping/stop_timeout/shutdown_failed/crashed), `winproc_windows.go` / `winproc_other.go` (process identity = PID + exe + start time) |
| Telnet | `telnet.go` (single `login` helper for Command/Shutdown) |
| Config XML | `xmlconfig.go` — saves edit value attributes **in the original bytes** (never re-encode the document) |
| Sandbox | `sandbox.go`, `web/js/sandbox-code.js` |
| Mods / saves / backups | `mods.go`, `saves.go`, `backup.go` (+ hot backups), `backup_restore.go`, shared ZIP safety in `zipimport.go` |
| Scheduler | `scheduler.go` (tasks, next-run maths, crash watcher), `api_schedule.go` (restart countdown, runners, autostart) |
| Players | `players.go` (lp parser, serveradmin.xml, commands, history), `api_players.go`, `naiwazi.go` (NaiwaziBot detection only — it is closed source, never bundle it) |
| Versions | `steambranch.go` (branch args, VDF reader, appmanifest), `versioncheck.go` + `api_versions.go` (GitHub release, SteamCMD app_info_print) |
| SteamCMD updates | `steamcmd.go` |
| Firewall | `firewall.go`, `firewall_windows.go` (PowerShell NetSecurity) |
| Performance | `performance.go` (pure calculations), `performance_windows.go` (PDH + kernel32; no helper process) |
| Events / console | `events.go` (SSE, events carry `time`), `console.go` |
| Frontend | `web/index.html`, `web/css/tokens.css` + `app.css`, `web/js/main.js` (router, top bar), `web/js/pages/*.js`, `web/js/locales/{zh-CN,zh-TW,en}.js` |

## Conventions and rules

- **Frontend**: vanilla ES modules, no build step, no CDN/fonts (strict CSP
  `script-src 'self'`). Build HTML with the `html` tagged template (auto-escapes).
  Every visible string goes in all three locale files — `web/tests/i18n.test.js`
  fails on a missing key or placeholder mismatch. Controls that depend on server
  state use `data-requires="stopped|running|force"` and are gated in place by
  `applyGates()`; never re-render a form on status events (loses user input).
  Design rules: `design-system/7dtd-panel/MASTER.md`.
- **API errors** return `{code, message}`; the UI localizes by `code`
  (`error_<code>` keys in locales). Add a locale entry for new codes.
- **Windows-specific code** lives in `*_windows.go` with a `*_other.go`
  counterpart so the package builds and tests on Linux. Use
  `golang.org/x/sys/windows`; load DLLs with `NewLazySystemDLL`.
- **Paths**: compare/join against the *canonical* root (`EvalSymlinks`). Windows
  paths can go through 8.3 short names or junctions (CI runner temp dir is
  `C:\Users\RUNNER~1\...`) — this broke all mod moves once.
- **Server state**: a crash is reported once as `crashed`, then `stopped`;
  durable signal is `ServerStatus.LastCrash` (kept until next start). Don't let
  UI logic depend on catching `crashed`.
- **panel.json** holds panel settings (listen, firewall, schedule, steam, noUpdateCheck, naiwaziPort);
  change it with `updatePanelConfig`. Never return `steam.branchPassword` to the browser.
- **Service rights**: only administrators may change the service configuration. Don't grant DC to the
  service SID or IU (it would allow switching the service to LocalSystem).
- Background loops (scheduler, crash watcher, version checks) start in `App.Serve` via
  `startBackground(a.lifetime)`; the first version check waits 2 minutes so tests never hit the network.
- Tests: behaviour tests, not source-string greps. `scriptTelnet` (scheduler_test.go) fakes a Telnet
  server per command. Test fakes touched by
  goroutines must be race-safe (atomics / locks).
- Commits: small, descriptive bodies explaining *why*; end with the
  Co-Authored-By / Claude-Session trailers given by the session.

## Known facts about the game

- Server exe `server\7DaysToDieServer.exe`, args `-quit -batchmode -nographics
  -dedicated -configfile=... -logfile logs\server-current.log`.
- World loaded when the log prints `StartGame done`. Log levels: `INF`, `WRN`,
  `ERR`, `EXC`. Build line: `INF Version: V 2.4 (b7) ...` → `V2.4-b7`.
- Graceful stop = Telnet `shutdown` (needs `TelnetEnabled`); player count from
  Telnet `lp` ("Total of N in the game").
- SteamCMD app id 294420; update keeps `serverconfig.xml`.

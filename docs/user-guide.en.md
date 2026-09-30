# User guide

[简体中文](user-guide.md) | English

The detailed companion to the [README](../README.en.md): how each page and feature behaves.

- [Folder layout](#folder-layout)
- [Starting and stopping](#starting-and-stopping)
- [Updates](#updates)
- [Schedule](#schedule)
- [Players](#players)
- [Performance](#performance)
- [Worlds and saves](#worlds-and-saves)
- [Backups and restore](#backups-and-restore)
- [Game config and translations](#game-config-and-translations)
- [SandboxCode](#sandboxcode)
- [Service, stopping and upgrading](#service-stopping-and-upgrading)
- [Firewall, mods and console](#firewall-mods-and-console)

## Folder layout

Everything the panel uses lives in the folder that contains `panel.exe`:

```text
server/                 official dedicated server files
userdata/               local game data
backups/config/         configuration backups
backups/userdata/       userdata ZIP backups (userdata-auto-* are made by scheduled tasks)
backups/before-restore/ userdata folders replaced by a restore (newest 3 kept)
cache/                  Sandbox definition cache, players seen (players.json)
logs/                   panel and server logs
tools/steamcmd/         SteamCMD
```

On first use, create the local folders from "Environment check" on Overview; one click also points
`UserDataFolder` at this `userdata`. The bottom of the sidebar switches between Simplified Chinese,
Traditional Chinese and English and between system, light and dark themes; the choice is saved in the
browser. Game config labels use the official `Localization.csv` translations where available, and the
raw XML key is always shown under the label.

## Starting and stopping

After a start the server shows as "Starting" until its log prints `StartGame done` (world loaded) or
Telnet answers; if neither happens within 10 minutes it switches to "Running" anyway. While loading, a
graceful stop is not possible (it needs Telnet), but a force stop is.

"Stop" asks the server over Telnet to save and exit, waiting up to 2 minutes. "Force stop" is available
while the server is starting, running, stopping, timed out or crashed, and only with a one-time
confirmation token from the backend; it needs confirming and should be used only when a graceful stop
fails, because unsaved data can be lost.

Before each start the previous run's `logs\server-current.log` is renamed to `logs\server-<time>.log`
(newest 5 kept) so crashes can be investigated.

## Updates

The Server page has two update actions. "Update game server" runs SteamCMD for App ID `294420` with
`validate`; "Update SteamCMD" downloads SteamCMD when it is missing or runs `+quit` to let it update
itself. Neither starts or stops the game server.

"Game version and branch" chooses the Steam branch to install: stable `public`, experimental
`latest_experimental`, or any branch name (for example the older `alpha21.2`) with an optional branch
password (`-betapassword`; kept in `panel.json` and never sent to the browser). "Default" passes no
`-beta`, as earlier versions did. After switching, stop the game and click "Update game server".

Update reminders: once a day the panel checks GitHub for a new panel release, and every six hours it
asks SteamCMD (`app_info_print`) for the latest build of the chosen branch and compares it with the
installed build in `server\steamapps\appmanifest_294420.acf`. Each new version is announced once in
the activity list. Automatic checks can be turned off on the Server page; you can also check by hand.

## Schedule

The Schedule page can add:

- **Restart**: at times of day (optionally on chosen weekdays) or every N minutes, announce the
  restart in game with `say` (for example 10, 5 and 1 minutes before; the text is customizable and
  `{minutes}` becomes the number), then stop gracefully over Telnet and start again;
- **Backup**: while the game runs it first runs `saveworld` ("hot" backup), otherwise it backs up
  directly; scheduled backups are named `userdata-auto-*` and pruned to "Backups to keep", while
  backups made by hand are never deleted automatically;
- **Command**, for example a regular `saveworld`; **Announcement**: a regular chat broadcast.

You can also enable "Restart the game after a crash" (10 seconds after a crash; after 3 restarts within
30 minutes it gives up, so a broken mod or save cannot loop forever) and "Start the game when the panel
starts" — together with "Start the panel with Windows" on the Panel page, the server comes back after a
reboot. Times are the panel computer's local time; a run missed by more than 5 minutes (sleep and so on)
is skipped rather than run late. The schedule is stored under `schedule` in `panel.json` and runs with
the panel (stopping the panel pauses it).

## Players

While the game runs, the Players page lists online players through Telnet `lp` (platform ID, level,
ping, IP) and can message, kick and ban them for a time; by platform ID (`Steam_…`, `EOS_…`, `XBL_…`)
it can whitelist, make admin or ban offline players; and it can announce to everyone. Every action is
one of the game's own console commands (`kick`, `ban add/remove`, `whitelist`, `admin`, `say`,
`sayplayer`). The admin, whitelist and ban lists are read from the game's `serveradmin.xml`; the panel
also remembers players seen while the game ran (`cache\players.json`) to make offline actions easier.

### NaiwaziBot

[NaiwaziBot](https://www.7risha.com/7038.html) is a server mod bundled by many hosting panels. It adds a
points shop, teleports, sign-ins and its own web back office (by default on the game port + 6);
documentation: <http://cn.naiwazi.com/bot/>. It is **free but not open source**: its
[GitHub repository](https://github.com/Naiwazi/NaiwaziBot) holds only a README with no source or licence,
the release is obfuscated .NET, and it connects to the author's cloud storage and auto-update. So this
panel **neither includes nor distributes** it. Upload its ZIP on the Mods page like any mod; the Players
page then detects it, shows its version, port and whether it is listening, opens its web panel and
shows the login file it writes (`server\NaiwaziBot_Data\NaiwaziBot_Password.txt`); the port can be set
by hand when it is not the default. Note that its web panel listens on all network interfaces and is not
covered by this panel's local-only protection.

## Performance

Every 2 seconds Overview shows host CPU, GPU, memory and the game process's CPU and memory, with a
chart of the last 60 seconds. The data comes straight from Windows performance counters (the same
source as Task Manager); nothing extra is installed or started. It samples only while Overview is
open, keeps no long-term history, has no alerts and does not read temperatures.

GPU usage needs the GPU Engine counters provided by the graphics driver. On some Windows Server
installations the counters are damaged or disabled; run `lodctr /R` in an administrator command
prompt to rebuild them.

## Worlds and saves

The Worlds & saves page lists worlds and saves, the ones in use and any that are missing. Switching,
exporting, importing and deleting work only while the server is stopped. An export is a ZIP package
with just the chosen world and save; an import accepts one such package; a ZIP backup is made before
deleting.

## Backups and restore

The Backups page makes ZIP backups of the whole `userdata`. While the game runs it first runs
`saveworld` and then copies the files; otherwise it backs up directly. Every backup can be
**restored** in one click (the game must be stopped): the backup is extracted to a temporary folder,
the current `userdata` is moved to `backups\before-restore\userdata-<time>` (newest 3 kept), and the
restored data takes its place; if that swap fails the original is put back. Backups can also be
deleted. Scheduled backups are set up on the Schedule page.

## Game config and translations

Game config keeps the original XML attributes, order and comments. Labels and descriptions come first
from the English, Simplified Chinese and Traditional Chinese texts in the official
`server/Data/Config/Localization.csv`; dedicated-server keys it does not cover use the panel's built-in
translations, and only when neither has text does the raw property name show. Built-in Chinese
descriptions are used only for entries that could be verified; unknown descriptions stay empty.

## SandboxCode

The raw `SandboxCode` can always be edited. When the server is running and the local cache matches its
build exactly, the page also shows visual controls constrained by the official Dashboard
`SandboxSettings` definitions: changing a control for an existing record updates the raw code at once,
and entering valid raw code updates the controls it defines. Unknown records and the original record
order are kept.

Nothing is written until you click Save. Visual-only changes submit exactly the changed options; valid
raw code you typed is submitted as is. Without a usable or matching definition the page is in raw mode:
no visual controls and no automatic rewriting. Invalid code is pointed out by record and character and
disables Save. When the cache does not match, the panel tries one automatic refresh for that build; the
"Dashboard token name" and "Dashboard token secret" boxes appear only when the Dashboard requires
authentication. The panel sends them to the local official Dashboard as the `X-SDTD-API-TOKENNAME` and
`X-SDTD-API-SECRET` headers; they live only in the page's memory, are cleared after a successful
refresh and are never stored in the panel, logs or cache.

## Service, stopping and upgrading

`panel.exe` manages the Windows service itself; the scripts of earlier versions (`scripts\`) are gone:

- **Double-click `panel.exe`**: without the service it runs in a console window and opens the browser;
  with the service installed it starts the service and opens the browser (if the service runs another
  `panel.exe`, an administrator prompt updates it to this one). Run as administrator it always runs in
  a console window, for admin-only tasks such as applying firewall rules.
- **Panel page**: version, run mode and panel folder; "Install service and switch", "Update service" and
  "Uninstall service" (with a UAC prompt); "Start the panel with Windows" (delayed automatic start);
  "Stop panel".
- **Command line**: `panel.exe service install`, `panel.exe service uninstall`,
  `panel.exe service autostart on|off` (they ask for administrator rights themselves), and
  `panel.exe --console` to force the console.

The service runs under its own virtual account `NT SERVICE\7DTDserverPanel`, which may modify only the
panel folder — not SYSTEM; the game server and mods inherit this limited identity. Signed-in users may
start and stop the service (double-clicking `panel.exe` needs no administrator prompt); only
administrators may change its configuration. So as a service the panel has no administrator rights: the
Firewall page can only preview, and "Start the panel with Windows" cannot be switched from the web page
(run `panel.exe service autostart on` in the panel folder instead). To apply firewall changes, stop the
panel on the Panel page, right-click `panel.exe` → "Run as administrator", apply, close it, and
double-click `panel.exe` to return to the service.

Stopping the panel stops only the web page, polling, scheduled tasks and log tailing — **the game keeps
running**; "Stop" and "Force stop" on the Server page stop the game gracefully or forcibly. Make a
userdata backup before upgrading.

If the browser does not open, check `logs\panel.log` or open the `http://127.0.0.1:<port>` address
printed at startup.

## Firewall, mods and console

The firewall manages only the panel's own inbound rules: by default the server's TCP port and UDP from
the base port to the base port + 3. Web Dashboard and Telnet stay local-only by default; exposing a
management interface needs an explicit risk confirmation. The panel never configures routers, NAT or
port forwarding.

Telnet may stay local-only without a password, but `TelnetPassword` must be set before opening its
firewall rule; custom TCP rules cannot cover enabled Dashboard/Telnet ports — use their own switches.

Mods live in `Mods` and `Mods.disabled` and can be switched only while the server is stopped. The
dependency policy is warn, strict or ignore: warn only warns, strict blocks a start with missing or
mismatched dependencies, ignore skips the check. Checks rely only on the metadata in local
`ModInfo.xml` files and do not replace the mod author's compatibility notes.

An uploaded mod ZIP may contain the mod folder at the top (`ModName\ModInfo.xml`), inside one extra
folder (for example `Mods\ModName\ModInfo.xml`), or `ModInfo.xml` at the root of the ZIP (the ZIP's name
then names the mod folder).

Mod ZIPs may expand to at most 16 GiB; mod and save packages may hold at most 100000 entries and must
leave at least 1 GiB free on the destination disk. Save packages keep a 256 GiB limit so large 16K
maps still fit.

The console opens on demand, keeps a bounded recent log and redacts secrets before storing lines;
Telnet accepts only restricted one-line commands. The panel binds to `127.0.0.1` by default.
Third-party licences are listed in `THIRD_PARTY_NOTICES.md`.

7 Days to Die Local Panel
Author: NightowlHaohao

For detailed Simplified Chinese instructions, open README.md and
docs/使用说明.md at https://github.com/NightOwlHaohao/7dtd-panel

Run panel.exe beside the server directory and use the loopback URL printed by
the program. It binds to 127.0.0.1 by default.

Service: panel.exe manages its own Windows service. Double-clicking panel.exe
runs it in a console window, or, once installed, starts the service and opens
the browser. The Panel page installs, updates and uninstalls the service and
switches delayed automatic start (each asks once for administrator approval);
from a command line: panel.exe service install|uninstall|autostart on|off, and
panel.exe --console forces the console. The service runs under the virtual
account NT SERVICE\7DTDserverPanel, which may modify only the panel folder
(not SYSTEM); signed-in users may start and stop it, only administrators may
reconfigure it. Because the service has no administrator rights, the Firewall
page can only preview; to apply firewall changes, stop the panel, run panel.exe
as administrator once, then double-click panel.exe again. Stopping the panel
only stops panel HTTP, polling, scheduled tasks and log tailing; the game keeps
running.

Schedule: timed restarts with in-game countdown announcements, backups (hot
while running, after saveworld; scheduled ones are pruned to a count),
console commands and announcements; optional restart after a crash (at most 3
times in 30 minutes) and starting the game when the panel starts.

Players: online players via lp; kick, timed bans, whitelist, admins, public and
private messages through the game's console commands; admin, whitelist and ban
lists from serveradmin.xml. NaiwaziBot (a closed-source server mod with its own
web panel on game port + 6) is detected and linked, not bundled.

Backups can be restored in one click while the game is stopped; the replaced
userdata is kept in backups\before-restore (newest 3).

Starting: the server shows "starting" until its log reports StartGame done or
Telnet answers (at most 10 minutes). The previous run's log is kept as
logs\server-<time>.log (newest 5).

Updates: the Server page has two actions. Update game server runs App ID 294420
with validate, on the chosen Steam branch (public, latest_experimental or any
branch name with an optional password). Update SteamCMD downloads it when
missing or runs +quit to self-update. Neither action stops or starts the game
automatically. The panel checks GitHub daily for a new panel release and
SteamCMD every six hours for a new game build (switchable).

Performance monitoring reads Windows performance counters directly (the same
source as Task Manager); nothing extra is installed or started. It samples only
while the Overview page is open and keeps no long-term history. GPU usage needs
driver-provided GPU Engine counters; on Windows Server, run lodctr /R as
administrator if the counters are missing.

Firewall: only panel-owned inbound rules are managed. Game defaults are the
server TCP port and UDP base through base+3. Dashboard and Telnet default to
local-only; exposing either management interface requires a risk confirmation.
Telnet exposure also requires a configured password, and custom TCP rules cannot
overlap enabled management ports. The panel never configures router/NAT/port forwarding.

Mods use Mods and Mods.disabled and can be switched only while the server is
stopped. Dependency policies are warn, strict and ignore; strict blocks missing
or version-mismatched dependencies. Checks are limited to available local
ModInfo.xml metadata and are not a full compatibility guarantee.
Mod ZIPs may contain mod folders, mod folders inside one wrapper folder, or
ModInfo.xml at the root (named after the ZIP). Mod ZIP expansion is limited to 16 GiB; Mod/save packages are limited to 100000
entries and must leave at least 1 GiB free on the destination disk.

The console is lazy, bounded and redacts sensitive values before storing logs.
Telnet accepts only restricted one-line commands. License: PolyForm Noncommercial 1.0.0 (see LICENSE) - free for
noncommercial use; commercial use (for example by hosting providers) needs
the author's permission. THIRD_PARTY_NOTICES.md records third-party licenses.

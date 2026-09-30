# 路线图与交接说明

最后更新：2026-09-30（v1.1.0 发布，准备公开仓库）。新会话请先读 `CLAUDE.md`，再读本文件。

## 1. 已完成：Windows 服务整合进 panel.exe

`scripts\` 已删除，服务由 `panel.exe` 自己管理（`servicectl.go`、`servicectl_windows.go`、`servicectl_other.go`）：

- 双击 `panel.exe`（`launchInteractive`）：未安装服务或以管理员运行 → 控制台模式并打开浏览器；
  已安装 → 必要时弹 UAC 更新服务路径/权限 → 启动服务 → 等端口可用 → 打开浏览器。
  以管理员运行但服务正在运行（占用端口）时给出提示并退出；出错时控制台窗口会等待回车再关闭。
- 命令行：`panel.exe service install|uninstall|autostart on|off`（非管理员时自动 UAC 提权，
  结果通过 `--result` 临时文件传回），`panel.exe --console`。`--check-service-paths` 已删除。
- API：`GET /api/panel`，`POST /api/panel/stop`，`POST /api/panel/service/install|uninstall`，
  `PUT /api/panel/service/autostart`。前端“面板”页（`web/js/pages/panel.js`）。
- 服务账户 `NT SERVICE\7DTDserverPanel`；DACL：SY/BA 完全控制、IU 查询/启动/停止、服务 SID 额外 DELETE。
  **只有管理员能改服务配置**（给服务 SID 或 IU 加 DC 会让服务能把自己改成 LocalSystem，属于提权漏洞），
  所以以服务运行时网页里不能切换开机自启，改用命令行 `panel.exe service autostart on`。
- 服务预检不再要求游戏文件存在（面板可以用 SteamCMD 下载游戏）。更新服务会保留启动类型。
- 发布包改名为 `7dtd-panel-<版本>-windows-x64.zip`。
- Windows CI 设置 `SEVENPANEL_SERVICE_TEST=1`，`TestServiceInstallRoundTrip` 用临时服务名安装 →
  检查 DACL、文件夹 ACL、自动启动与更新后保留 → 卸载。

仍需在真机上确认：UAC 弹窗流程（安装并切换、双击时更新旧服务路径）、开机延迟自启后游戏自动开服。

## 2. 已完成的功能（原列表 1–7）

1. **备份恢复**（`backup.go`、`backup_restore.go`）：一键恢复（当前 userdata 移到
   `backups\before-restore`，保留 3 份，换入失败自动回滚）、删除备份；游戏运行时可热备份（先 `saveworld`）。
2. **崩溃自动重启、定时重启**（`scheduler.go`、`api_schedule.go`）：计划任务支持重启（游戏内 `say` 倒计时，
   文字可自定义）、备份（`userdata-auto-*`，按份数清理）、控制台命令、公告；崩溃自动重启（30 分钟内最多 3 次）。
3. **SteamCMD 分支选择**（`steambranch.go`）：`panel.json` 的 `steam.branch/branchPassword`，含简单的 VDF 解析器。
4. **玩家管理**（`players.go`、`api_players.go`）：`lp` 在线列表、踢出/封禁/白名单/管理员/私聊/公告，
   `serveradmin.xml` 列表，见过的玩家（`cache\players.json`）。
   **NaiwaziBot**（`naiwazi.go`）：用户希望整合 https://www.7risha.com/7038.html 。调查结论：它的核心
   `main.bin`/`1v.dll` 是混淆过的 .NET 程序集，网页 JS 也混淆了，没有许可证，并且会连接作者云端和自动更新，
   所以**不是开源软件，不能打包进本仓库**。之后用户又给了 https://github.com/Naiwazi/NaiwaziBot 和
   http://cn.naiwazi.com/bot/ ：GitHub 仓库只有一次提交、一个两行的 README（“永久免费可自主编程”），
   没有源码和许可证，即“免费软件”而不是开源；文档站从云端会话访问不了。已实测：用户提供的
   `Naiwazi面板.zip`（外层文件夹 + `NaiwaziBot\` + 说明 txt）可以直接在“Mod 管理”页上传安装并被检测到。现在的做法是“联动”：检测 Mods 中的 NaiwaziBot、推算网页端口
   （游戏端口 + 6，可手动覆盖）、检查是否监听、一键打开、显示它写的登录信息文件。
5. **开机自启**：服务延迟自动启动（“面板”页或命令行）+ 计划任务页“面板启动时自动启动游戏”。
6. **更新提醒**（`versioncheck.go`、`api_versions.go`）：GitHub Release 每天、SteamCMD `app_info_print`
   每 6 小时（与更新互斥）；每个新版本只提醒一次；可关闭。仓库私有时 GitHub 检查会报“找不到公开发布”。
7. **定时任务**：见第 2 项。

## 3. 可以增加的功能（按优先级）

1. **Discord / Webhook 通知**：开服、关服、崩溃、玩家进出、计划任务失败、发现更新。
2. **仓库公开**（进行中）：许可证已改为 PolyForm Noncommercial 1.0.0（禁止商用），删除了编译产物和带个人路径的
   旧 AI 规划文档，文档拆为 README / 使用说明 / 开发说明，并加了 SECURITY.md 和 Issue 模板。
   为了不公开旧提交历史（含作者邮箱和本机路径），采用“新建公开仓库、只放清理后的代码”的方式；旧仓库保留为私有存档。
3. **多服务器实例**：一个面板管理多份 server 目录和端口。
4. **玩家管理增强**：解析 `players.xml`/存档里的离线玩家、`ban list`/`admin list` 的 Telnet 输出（不依赖文件）、
   传送/给物品等常用命令模板。
5. **计划任务增强**：运行历史持久化（现在只在内存里）、任务失败重试、按在线人数跳过重启。
6. **备份到其他磁盘/网络位置**，备份大小与磁盘空间预警。

## 4. 已知限制
- GPU 使用率在 CI 上无法验证（没有显卡），需要在真机上确认。
- 以服务运行时，“打开备份目录”无法弹出资源管理器窗口，会显示目录路径。
- 热备份在游戏写文件时复制，极少数情况下个别文件可能不一致；重要操作前建议停服备份。
- 玩家命令格式按 V1.x/V2.x（`pltfmid=`、`<users>`）编写，并兼容 A20 的 `steamID`；新版本改格式时需要调整解析。
- 计划任务的时间用面板所在电脑的本地时区；错过 5 分钟以上的任务会跳过。

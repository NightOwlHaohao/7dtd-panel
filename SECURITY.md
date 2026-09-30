# 安全说明 / Security

中文在前，[English below](#security-design).

## 设计上的保护

- 面板只监听 `127.0.0.1`，只接受本机访问，并校验 Host 与 Origin。
- 所有会修改数据的请求都需要页面加载时获得的随机令牌（每次启动面板都会重新生成）。
- 以 Windows 服务运行时使用受限的虚拟账户 `NT SERVICE\7DTDserverPanel`，只能修改面板文件夹，不是 SYSTEM；只有管理员能修改服务配置。
- 敏感信息（Telnet 密码、分支密码、Dashboard 令牌）不会写入日志，也不会返回给浏览器。

## 报告安全问题

如果你发现了可能被利用的安全问题，**请不要公开提交 Issue**。请通过 GitHub 的
**Security → Report a vulnerability**（私密漏洞报告）联系作者，并说明：

- 面板版本（侧栏底部或“面板”页可以看到）；
- 问题的表现和复现步骤；
- 可能造成的影响。

作者会尽快回复；修复发布后会在 Release 说明中致谢（如果你愿意）。

---

## Security design

- The panel listens on `127.0.0.1` only, accepts local requests only and checks Host and Origin.
- Every request that changes data needs a random token the page receives when it loads (a new one each time the panel starts).
- As a Windows service it runs under the restricted virtual account `NT SERVICE\7DTDserverPanel`, which may modify only the panel folder (not SYSTEM); only administrators may change the service configuration.
- Secrets (Telnet password, branch password, Dashboard token) are never written to logs or returned to the browser.

## Reporting a vulnerability

Please **do not open a public issue** for security problems. Use GitHub's private
**Security → Report a vulnerability** form and include:

- the panel version (bottom of the sidebar or the Panel page);
- what happens and how to reproduce it;
- the possible impact.

The author will reply as soon as possible and, if you wish, credit you in the release notes of the fix.

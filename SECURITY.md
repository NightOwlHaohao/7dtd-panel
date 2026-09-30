# 安全说明 / Security

## 设计上的保护

- 面板只监听 `127.0.0.1`，只接受本机访问，并校验 Host 与 Origin。
- 所有会修改数据的请求都需要页面加载时获得的随机令牌（每次启动面板都会重新生成）。
- 以 Windows 服务运行时使用受限的虚拟账户 `NT SERVICE\7DTDserverPanel`，只能修改面板文件夹，不是 SYSTEM。
- 敏感信息（Telnet 密码、分支密码、Dashboard 令牌）不会写入日志，也不会返回给浏览器。

## 报告安全问题

如果你发现了可能被利用的安全问题，**请不要公开提交 Issue**。请通过 GitHub 的
**Security → Report a vulnerability**（私密漏洞报告）联系作者，说明：

- 面板版本（侧栏底部或“面板”页可以看到）；
- 问题的表现和复现步骤；
- 可能造成的影响。

作者会尽快回复。修复发布后会在 Release 说明中致谢（如果你愿意）。

## Reporting a vulnerability

Please do not open a public issue for security problems. Use GitHub's private
**Security → Report a vulnerability** form with the panel version, steps to
reproduce and the impact.

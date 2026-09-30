# 开发说明

简体中文 | [English](development.en.md)

给想修改或自己编译面板的人。项目约定、结构和常见坑见根目录的 [`CLAUDE.md`](../CLAUDE.md)（英文），
进行中的工作和想法见 [路线图](ROADMAP.md)。

## 结构

后端是单个 Go 程序（`go 1.26`，唯一依赖 `golang.org/x/sys`），前端是不需要构建的原生 ES 模块，
通过 `go:embed` 打包进 `panel.exe`：

```text
cmd/panel/              后端（package main）
  *_windows.go          Windows 专属实现；*_other.go 为其他系统的替代实现，便于在 Linux 上测试
  *_test.go, testdata/  后端测试
web/                    网页界面（web/embed.go 把它打包进 panel.exe）
  index.html            页面骨架（侧栏、顶栏、控制台抽屉、确认对话框）
  css/tokens.css        颜色、字号、间距等设计变量（浅色/深色）
  css/app.css           组件与布局样式
  js/main.js            启动、路由（#/overview 等）与顶栏状态
  js/pages/*.js         每个页面一个模块
  js/locales/*.js       三种语言的全部界面文本
  tests/*.test.js       前端单元测试（node --test，不会打包进程序）
docs/                   文档、截图和界面设计规范（docs/design-system/）
build.ps1               Windows 发布构建脚本
```

## 检查与测试

提交前请确保以下命令全部通过（CI 会执行同样的检查）：

```sh
gofmt -l .                                   # 不应输出任何文件
go vet ./... && GOOS=windows go vet ./...    # 两个平台都要通过
go test -race -count=1 ./...                 # Linux 上也可运行，Windows 专属测试会跳过
cd web && node --test                        # 前端（Node 22，无第三方依赖）
```

本地编译：`go build -o panel.exe ./cmd/panel`（在 Windows 上），或 `GOOS=windows go build -o panel.exe ./cmd/panel`。

## 构建与发布

发布包用 `build.ps1` 在 Windows 上生成：先跑测试，再用 `git describe` 得到的版本号（例如
`v1.4.0`）编译 `panel.exe`，并把说明文档和许可证一起放进 `dist\release`。版本号会显示在启动日志
和侧栏底部。每次推送都会由 GitHub Actions 在 Windows 和 Linux 上运行同样的检查，Windows 任务
还会执行 `build.ps1`、真实安装并卸载一个临时 Windows 服务，并把 `dist\release` 作为构建产物上传。

发布新版本：在 GitHub 的 Actions → Release 页面点 “Run workflow”，填入版本号（例如 `v1.2.0`）；
或者在 `main` 上打同名标签并推送。GitHub Actions 会在 Windows 上测试、构建，打上版本标签，
并自动创建 GitHub Release，附上 `7dtd-panel-<版本>-windows-x64.zip`。

## 界面文本与文档

- 所有界面文字都要同时写进 `web/js/locales/` 的 `zh-CN.js`、`zh-TW.js`、`en.js`，缺少任何一个测试都会失败。
- 文档以简体中文为主，并提供英文版（文件名加 `.en`，例如 `user-guide.en.md`）；修改时请同步两个版本。

## 贡献

欢迎提交 Issue 和 Pull Request。本项目使用
[PolyForm Noncommercial 1.0.0](../LICENSE) 许可证：提交代码即表示你同意你的改动以同样的许可证发布。

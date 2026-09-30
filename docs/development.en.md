# Development

[简体中文](development.md) | English

For people who want to change or build the panel themselves. Conventions, layout and known
pitfalls are in [`CLAUDE.md`](../CLAUDE.md); open work and ideas are in the [roadmap](ROADMAP.en.md).

## Layout

The backend is one Go program (`go 1.26`, the only dependency is `golang.org/x/sys`); the
frontend is plain ES modules with no build step, embedded into `panel.exe` with `go:embed`:

```text
cmd/panel/              backend (package main)
  *_windows.go          Windows implementations; *_other.go stand-ins so it builds and tests on Linux
  *_test.go, testdata/  backend tests
web/                    browser UI (web/embed.go embeds it into panel.exe)
  index.html            page shell (sidebar, top bar, console drawer, confirm dialog)
  css/tokens.css        design tokens: colours, type, spacing (light/dark)
  css/app.css           components and layout
  js/main.js            boot, router (#/overview …) and top bar
  js/pages/*.js         one module per page
  js/locales/*.js       every UI string in three languages
  tests/*.test.js       frontend unit tests (node --test; not embedded)
docs/                   documentation, screenshots and the UI design rules (docs/design-system/)
build.ps1               Windows release build
```

## Checks and tests

Before sending changes make sure these pass (CI runs the same):

```sh
gofmt -l .                                   # must print nothing
go vet ./... && GOOS=windows go vet ./...    # both platforms
go test -race -count=1 ./...                 # runs on Linux; Windows-only tests skip
cd web && node --test                        # frontend (Node 22, no dependencies)
```

Local build: `go build -o panel.exe ./cmd/panel` on Windows, or `GOOS=windows go build -o panel.exe ./cmd/panel`.

## Build and release

`build.ps1` builds the release on Windows: it runs the tests, compiles `panel.exe` stamped with
the version from `git describe` (for example `v1.4.0`) and copies the documents and licences into
`dist\release`. The version shows in the startup log and at the bottom of the sidebar. Every push
runs the same checks on Windows and Linux in GitHub Actions; the Windows job also runs
`build.ps1`, installs and removes a throwaway Windows service, and uploads `dist\release`.

To release: Actions → Release → "Run workflow" with a version such as `v1.2.0`, or push a tag of
that name on `main`. The workflow tests, builds, tags and creates the GitHub Release with
`7dtd-panel-<version>-windows-x64.zip` attached.

## UI text and documentation

- Every visible string goes into all three of `web/js/locales/zh-CN.js`, `zh-TW.js` and `en.js`;
  a missing key fails the tests.
- Documents are written in Simplified Chinese with an English version (`.en` in the file name,
  for example `user-guide.en.md`); please update both.

## Contributing

Issues and pull requests are welcome. The project is licensed under
[PolyForm Noncommercial 1.0.0](../LICENSE); by contributing you agree that your changes are
released under the same licence.

# Agent instructions

This repository's project memory lives in **`CLAUDE.md`** (layout, commands,
conventions, known pitfalls) and open work in **`docs/ROADMAP.md`**. Read both
before making changes. The owner writes in Chinese; reply in Chinese.

Before pushing: `gofmt -l .` prints nothing, `go vet ./...` and
`GOOS=windows go vet ./...` pass, `go test -race ./...` passes, and
`cd web && node --test` passes.

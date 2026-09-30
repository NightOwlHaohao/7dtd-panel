# 7DTD Panel design system

Applies to every page of the web UI. Vanilla HTML, CSS and ES modules only; no UI framework, web fonts or CDN assets (the panel must work offline and under a strict CSP).

## Basis

- Product: local Windows game-server administration dashboard, used on desktop first and occasionally from a phone on the LAN.
- Style: calm, dense, technical. Dark and light themes are both first-class; the accent is a restrained "ember" orange that nods to the game without decorating.
- Source: the `ui-ux-pro-max` skill's priority rules (accessibility → interaction → layout → typography/colour → motion → forms → navigation → charts). Its search script has not been run for this project yet, so these tokens come from the skill's built-in defaults rather than a database match; re-run `--design-system` and adjust `web/css/tokens.css` when it is available.
- Charts follow the dataviz method: categorical slots 1–3 (validated for both themes; light slot 3 is below 3:1, so every line is also labelled at its end and the stat tiles act as the table view).

## Tokens (`web/css/tokens.css`)

Components reference roles only, never raw colours: `--bg`, `--surface`, `--surface-2`, `--surface-3`, `--border`, `--border-strong`, `--fg`, `--fg-muted`, `--fg-subtle`, `--accent` (+ `-hover`, `-fg`, `-soft`, `-text`), `--success`/`--warning`/`--danger`/`--info` (+ `-soft`), `--focus`, `--series-1..3`, `--chart-grid`, `--log-bg`, `--log-fg`.

Dark values apply for the OS setting unless the viewer picked light (`:root:where(:not([data-theme="light"]))`) and always under `:root[data-theme="dark"]`.

Type: system UI stack with Chinese fallbacks, 15px body, 13px minimum for metadata, monospace for logs, paths and XML keys. Spacing: 4/8px rhythm (`--space-1..6`). Controls: 40px, 44px on coarse pointers.

## Layout

- App shell: fixed-width sidebar (grouped navigation: 运行 / 配置 / 数据 / 系统) + sticky top bar showing server state, uptime, build and the console toggle.
- Content capped at 80rem; cards (`.card`, `.card-header`, `.card-body`) group each task.
- Below 62rem the sidebar becomes an off-canvas drawer; below 40rem all grids collapse to one column. No page-level horizontal scroll; wide tables scroll inside `.table-wrap`.

## Behaviour rules

- Server state is never colour-only: `stateBadge()` pairs a dot with text.
- Controls that depend on server state declare `data-requires="stopped|running|force"` and are enabled or disabled in place by `applyGates()`, so a state change never re-renders a form the user is editing.
- Destructive or irreversible actions (force stop, delete save, delete rule, updates) go through the shared confirm dialog; the danger variant is used for data loss.
- Async buttons use `withBusy()`: spinner + `aria-busy`, restored afterwards; failures surface as toasts.
- Editors with unsaved changes (config, sandbox) show a sticky save bar with a change count, highlight changed rows, and guard navigation away.
- Every visible string lives in `web/js/locales/*.js`; tests fail if a key is missing in any language.
- Motion is feedback only (≤180ms) and is disabled under `prefers-reduced-motion`.

Page files under `pages/` record page-specific decisions only.

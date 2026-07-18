# NOTES

## Step 1: three-state theme toggle (done 2026-07-17)

- Theme block lives in `src/clients/plan_status_page.html` main `<script>` after `const el = (id) => ...`, before `fmtTime`.
- Ring keys off `document.documentElement.dataset.theme` (stored/forced state), NOT effective theme. Unset or bogus value advances to "light". From "dark", click lands on auto (deletes `dataset.theme`, removes `localStorage` key "theme").
- Auto state has no `data-theme` attribute; CSS `prefers-color-scheme` + `:root:not([data-theme="light"])` rules drive appearance. No `matchMedia` listener anymore — system-preference changes apply via CSS alone, no JS re-render needed in auto (icon is ◐ regardless).
- `renderThemeToggle()` called after try/catch so icon updates even when localStorage throws (private-mode Safari etc.).
- Exactly two `} catch (e) {}` in file: anti-flash IIFE in `<head>` (lines ~188-196, untouched) and click handler. Later steps must not add a third without updating any grep-count checks.
- CSS, header markup (`#theme-toggle` button, initial ☾ glyph), anti-flash script all byte-identical to before.
- Verified: `go build -o ./bin/determined ./cmd/determined` passes.

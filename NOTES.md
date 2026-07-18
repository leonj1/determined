# NOTES

## Step 1: three-state theme toggle (done 2026-07-17)

- Theme block lives in `src/clients/plan_status_page.html` main `<script>` after `const el = (id) => ...`, before `fmtTime`.
- Ring keys off `document.documentElement.dataset.theme` (stored/forced state), NOT effective theme. Unset or bogus value advances to "light". From "dark", click lands on auto (deletes `dataset.theme`, removes `localStorage` key "theme").
- Auto state has no `data-theme` attribute; CSS `prefers-color-scheme` + `:root:not([data-theme="light"])` rules drive appearance. No `matchMedia` listener anymore — system-preference changes apply via CSS alone, no JS re-render needed in auto (icon is ◐ regardless).
- `renderThemeToggle()` called after try/catch so icon updates even when localStorage throws (private-mode Safari etc.).
- Exactly two `} catch (e) {}` in file: anti-flash IIFE in `<head>` (lines ~188-196, untouched) and click handler. Later steps must not add a third without updating any grep-count checks.
- CSS, header markup (`#theme-toggle` button, initial ☾ glyph), anti-flash script all byte-identical to before.
- Verified: `go build -o ./bin/determined ./cmd/determined` passes.

## Step 2: theme contract in assertPageServed (done 2026-07-17)

- Appended theme assertions to end of `assertPageServed` in `tests/plan_status_server_test.go`, after 30-marker loop. No import change (`strings` already there).
- Contract locks in: 7 presence markers, absence of `effectiveTheme`/`darkQuery`, anti-flash `localStorage.getItem("theme")` index before `<body>` index, `} catch (e) {}` count ≥ 2, 13 dark declarations each count == 2 (dark block + media query), 12 light declarations present.
- Dark declarations use exact-count-2 checks — adding a third dark palette block or any inline duplicate of these strings breaks the test. Light `color-scheme: light;` is presence-only (page has exactly one).
- `go test -count=1 ./tests/ -run TestPlanStatusServerContract` passes.

## Step 3: full test suite (done 2026-07-17)

- `go test -count=1 ./...` exits zero. All 5 packages pass: cmd/determined, src/clients, src/models, src/services, tests (includes TestPlanStatusServerContract).
- No pre-existing failures anywhere; nothing to report out of scope.

## Step 4: browser smoke test (done 2026-07-17)

- Ran via agent-browser (Chromium) against `./bin/determined -plan "test" -interactive` at http://localhost:59841/. All criteria (a)–(g) pass; automated in-browser verification stood in for the manual gate per CLAUDE.md's agent-browser rule.
- (a) No stored key: ◐, no `data-theme`, `colorScheme` follows emulated `prefers-color-scheme` both ways (`agent-browser set media light|dark`): light bg rgb(246,247,249), dark bg rgb(16,20,24).
- (b) Clicks cycle ◐ → ☀ → ☾ → ◐ with immediate restyle; titles: "Theme: light — click for dark", "Theme: dark — click for auto", "Theme: auto — click for light".
- (c) Reload at ☀: loads light, `theme`=`light`; one click lands ☾ (not ◐).
- (d) Reload at ☾: loads dark; one click lands ◐, key removed, `data-theme` attribute gone.
- (e) `theme`=`bogus` + reload: auto ◐, zero page errors; one click lands ☀.
- (f) `colorScheme` = "light" forced light, "dark" forced dark, OS value in auto.
- (g) SSE content renders throughout: plan panel shows PLAN.md content, activity log populates ("assessing plan"), survives reloads/toggles.
- Gotcha: killing the server via pkill exits 144 — expected shutdown, not a crash. Second engine (Firefox) not run; bonus only, not a gate.

## Quality gate: build check (done 2026-07-17)

- `go build -o ./bin/determined ./cmd/determined` exits zero from repo root. No code changes needed; binary at `./bin/determined` is fresh.

## Quality gate: contract test (done 2026-07-17)

- `go test -count=1 ./tests/ -run TestPlanStatusServerContract` exits zero (`ok determined/tests 0.222s`). All Step 2 assertions active: 30 pre-existing + 7 theme presence markers (37 total), 2 absence checks (`effectiveTheme`, `darkQuery`), anti-flash-before-`<body>` position, `} catch (e) {}` count ≥ 2, 13 dark declarations exact-count-2, 12 light declarations present. No code changes needed — verification only.

# PLAN: Theme Toggle for Interactive-Mode Status Page

## Task Type
Feature + UI (single-page enhancement to the embedded HTML status page).

## Intended Outcome
The live HTML status page served by `determined -plan ... -interactive`
(`src/clients/plan_status_page.html`, embedded via `//go:embed` in
`src/clients/plan_status_server.go`) gains a user-facing theme toggle so the
viewer can override the current system-driven light/dark styling.

## Target User / Use Case
A developer running a planning session with `-interactive` who watches the
status page in a browser and wants the page in a specific theme regardless of
their OS `prefers-color-scheme` setting (e.g. dark OS but wants a light page
for a screenshot, or vice versa).

## Current Behavior
The page defines light-theme CSS variables on `:root` and overrides them
inside a `@media (prefers-color-scheme: dark)` block. Theme follows the OS
only; there is no user control.

## Proposed Behavior
- A toggle button in the page header (right side, next to the git info)
  cycling through three states in a fixed circular order: **auto → light →
  dark → auto**. From any current state, one click advances to the next state
  in this ring — e.g. if a previous session stored `"dark"`, the first click
  transitions to `"auto"` (not `"light"`).
  - **auto**: follow `prefers-color-scheme` (today's behavior; the default).
  - **light** / **dark**: force that theme.
- Implementation: restructure CSS so the dark variable set is reachable two
  ways — via the media query when in auto mode (the existing `:root` selector
  inside `@media (prefers-color-scheme: dark)` is replaced with
  `html:not([data-theme="light"])` so a forced-light choice beats a dark OS),
  and via an explicit `html[data-theme="dark"]` selector when forced. Forced
  light uses `html[data-theme="light"]` (the `:root` defaults already provide
  light values; the `[data-theme="light"]` selector exists only for
  `color-scheme` and symmetry). JS sets `data-theme` on `<html>` and updates
  the button label to show the active state.
- Persistence: chosen state saved to `localStorage` (key `determined-theme`);
  restored on page load before first paint (small inline script in `<head>`
  placed before the `<style>` block to avoid a flash of wrong theme). `auto`
  clears the stored key.
- `localStorage` resilience: all reads and writes are wrapped in try/catch.
  When `localStorage` is unavailable (privacy-restricted contexts, Safari
  private browsing, storage-full conditions), the toggle silently degrades to
  auto-only behavior — the button still cycles visually but the choice is not
  persisted across reloads, and the page never throws an uncaught script error.
- `color-scheme` property kept correct per state so form controls/scrollbars
  match (`light dark` in auto, `light` under `[data-theme="light"]`, `dark`
  under `[data-theme="dark"]`).

## In Scope
- Edits to `src/clients/plan_status_page.html` only (CSS + markup + JS).
- A Go test asserting the served page contains the toggle contract (see
  Validation).

## Out of Scope
- No changes to the SSE protocol, `plan_status_server.go` handlers, status
  model, or CLI flags.
- No new themes beyond light/dark/auto.
- No server-side persistence of theme choice.

## Constraints
- Page is a single self-contained embedded HTML file — no external assets,
  no build step, no frameworks. All CSS/JS stays inline.
- Must not disturb the existing SSE rendering logic (`render`,
  `renderBanner`, `EventSource` wiring).
- Existing visual design (CSS variable palette) unchanged; toggle only
  switches which variable set applies.
- Repo conventions: this is static asset content inside an existing client;
  no new Go classes needed, so the I/O-interface/Fake rules are not
  triggered. The one production-code touchpoint (embed) already exists.

## Assumptions (defaults chosen; flag if wrong)
1. Three-state toggle (auto/light/dark) with `auto` as default — preserves
   today's behavior for users who never touch it.
2. `localStorage` persistence is desired (standard UX for theme toggles).
3. A simple text/emoji button (e.g. "◐ Auto" / "☀ Light" / "🌙 Dark") is
   acceptable; no icon assets.
4. No CRITERIA.md exists, so no BDD journey tests are required; validation is
   the Go contract test plus manual browser check.

## Material Risks
- **Flash of wrong theme on load**: mitigated by placing the anti-flash inline
  `<head>` script before the `<style>` block. Correctness is verified
  mechanically by a Go test assertion that the substring `"determined-theme"`
  appears before `<style>` in the served page source.
- **CSS duplication drift**: the dark variable set must appear in two places
  (media query block for auto mode and `html[data-theme="dark"]` block for
  forced dark). Both blocks carry a sync-warning comment pointing to each
  other, and both live next to each other in a fixed order: light `:root`
  variables, then media-query dark block, then `html[data-theme="dark"]`
  dark block, then `color-scheme` overrides. The contract test (Step 5)
  programmatically extracts the dark variable values from both blocks and
  asserts they are identical — a mismatch fails the test even if both blocks
  individually contain valid CSS.
- **localStorage throws at runtime**: mitigated by try/catch wrappers in both
  the anti-flash `<head>` script and the toggle click handler. A storage
  failure degrades silently to auto-only behavior with no uncaught exceptions.
- **Breaking the embedded-page contract test**: run
  `go test -count=1 ./tests/ -run TestPlanStatusServerContract` after the
  edit.

## Observable Success Criteria
1. Page loads with no stored preference: theme follows OS setting (both OS
   modes verified).
2. Clicking the toggle cycles auto → light → dark → auto; page restyles
   immediately without reload; button label reflects the active state.
3. Forced choice survives a page reload (localStorage).
4. Selecting auto returns the page to OS-driven behavior and clears storage.
5. Existing behavior intact: SSE updates still render goal/plan/steps/banner;
   `go test -count=1 ./...` passes.
6. When localStorage is unavailable, the page loads without error, the toggle
   still cycles visually, and no uncaught exception appears in the console.
7. When a bogus value is stored (any string other than `"light"` or `"dark"`),
   the page degrades to auto mode without error.
8. `color-scheme` computed style on `<html>` is `"light dark"` in auto mode,
   `"light"` under forced light, and `"dark"` under forced dark — verified in
   DevTools with `getComputedStyle(document.documentElement).colorScheme`.

## Validation Approach
- **Contract test (Step 5)**: extend the existing `TestPlanStatusServerContract`
  in `tests/plan_status_server_test.go` to assert the served page contains
  all six existing regression markers (`"determined — planning"`,
  `"EventSource"`, `"banner"`, `"step-card"`, `"taskSteps"`,
   `"Done when: "`) plus: `id="theme-toggle"`, `html:not([data-theme="light"])`,
   `html[data-theme="dark"] {`, `"determined-theme"`,
   `color-scheme: light dark`, `html[data-theme="light"] { color-scheme: light; }`,
   `html[data-theme="dark"] { color-scheme: dark; }`, and `"KEEP IN SYNC"`
   (asserted via `strings.Count` to appear exactly 2 times). The marker
   `html[data-theme="light"] { color-scheme: light; }` is used instead of
   `color-scheme: light` because the latter is a substring of the existing `:root`
   `color-scheme: light dark` declaration and would pass even without the
   forced-light override block. Similarly, `html[data-theme="dark"] { color-scheme: dark; }`
   is used instead of bare `color-scheme: dark` to avoid matching unrelated
   future CSS properties, comments, or JS strings containing that substring.
   Also assert the substring `"determined-theme"` appears before `<style>`
   (proving the anti-flash script runs before CSS is parsed).
- **CSS variable value assertions (Step 5)**: the contract test also verifies
   that the `:root` block contains the expected light-theme variable values
   (preserving today's palette), and that the two dark-theme blocks (media
   query `html:not([data-theme="light"])` and `html[data-theme="dark"]`)
   contain identical variable values. The `extractDarkVars` function locates
   each dark block by its unique `KEEP IN SYNC` comment, scans backward from
   `--bg:` to find the block's opening `{`, then extracts `--var: value;`
   pairs into maps compared with `reflect.DeepEqual`. This catches CSS
   duplication drift and selector mismatches that page-wide occurrence counting
   would miss, without coupling the test to cosmetic whitespace differences.
- **Regression gate (Step 6)**: `go test -count=1 ./...` passes.
- **Manual smoke (Step 7)**: verify all eight Observable Success Criteria in at
  least one Chromium-based browser and one Firefox-based browser. If any
  criterion fails, diagnose which step caused the failure and re-run from
  that step forward — do not blindly restart from Step 1.

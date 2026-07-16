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
  cycling through three states: **auto → light → dark → auto**.
  - **auto**: follow `prefers-color-scheme` (today's behavior; the default).
  - **light** / **dark**: force that theme.
- Implementation: restructure CSS so the dark variable set is reachable two
  ways — via the media query when in auto mode (scoped to
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
  dark block, then `color-scheme` overrides.
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

## Validation Approach
- Extend the existing `TestPlanStatusServerContract` in
  `tests/plan_status_server_test.go` to assert the served page contains:
  `id="theme-toggle"`, `html[data-theme`, `"determined-theme"`, and that the
  substring `"determined-theme"` appears before `<style>` (proving the
  anti-flash script runs before CSS is parsed). Use `-count=1` to bypass Go's
  test cache.
- Manual browser verification of the five success criteria using
  `determined -plan "<goal>" -interactive` in both OS color schemes.
- Regression: full `go test -count=1 ./...` and `make test` pass.
- If manual smoke testing (Step 7) uncovers a bug, return to Step 1 and
  repeat the full step sequence through Step 7.

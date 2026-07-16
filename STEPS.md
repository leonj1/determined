# STEPS: Theme Toggle for Interactive-Mode Status Page

- [ ] 1. Restructure theme CSS in `src/clients/plan_status_page.html`.
  **CSS block ordering (top to bottom):**
  (a) Keep the existing `:root` light variable set and `color-scheme: light dark` exactly as they are.
  (b) Scope the existing `@media (prefers-color-scheme: dark)` block to `html:not([data-theme="light"])` so a forced-light choice beats a dark OS. Retain all dark variable overrides inside this block. Add a comment above it:
  `/* KEEP IN SYNC with the dark variable block in the html[data-theme="dark"] selector below */`
  (c) Add a new `html[data-theme="dark"]` block immediately after the media query block, containing a copy of the same dark variable overrides. Add a comment above it:
  `/* KEEP IN SYNC with the dark variable block in the media query above */`
  (d) After both dark blocks, add `color-scheme` overrides:
  `html[data-theme="light"] { color-scheme: light; }`
  `html[data-theme="dark"]  { color-scheme: dark;  }`
  **Done when:** With no `data-theme` attribute the page renders identically to the current page in both OS color schemes (verify in browser DevTools by toggling emulated `prefers-color-scheme` and confirming no visual difference from before). Setting `data-theme="dark"` on `<html>` via DevTools forces dark styling under a light OS. Setting `data-theme="light"` forces light styling under a dark OS.

- [ ] 2. Add an anti-flash inline script in `src/clients/plan_status_page.html`.
  **Prerequisite:** Step 1 (CSS selectors for `html[data-theme]` must exist).
  Place a `<script>` block at the top of `<head>`, **before** the `<style>` block:
  ```js
  (function(){var t=localStorage.getItem("determined-theme");if(t==="light"||t==="dark")document.documentElement.dataset.theme=t;})();
  ```
  All other values (missing key, `null`, `"auto"`, bogus strings) must leave `data-theme` unset.
  **Done when:** The Go contract test in Step 5 verifies that the substring `"determined-theme"` appears before `<style>` in the served page source (mechanical proof the script parses before CSS). Manually: with `localStorage.setItem("determined-theme","dark")`, inspect `<html>` after a hard reload — the `data-theme="dark"` attribute must be present before any paint.

- [ ] 3. Add a theme toggle button to the header in `src/clients/plan_status_page.html`.
  **Prerequisite:** Step 1 (CSS variables `--card`, `--border`, `--fg` must exist for styling).
  After the `.git` div inside `<header>`, add:
  `<button id="theme-toggle" type="button" aria-label="Switch color scheme">◐ Auto</button>`
  Style it inline via a `<style>` addition: `#theme-toggle { background: var(--card); color: var(--fg); border: 1px solid var(--border); border-radius: 0.4rem; padding: 0.25rem 0.6rem; font-size: 0.85rem; cursor: pointer; }` (and matching `:hover` / `:focus-visible` states with `--accent`).
  This step adds **static markup only** — no JS behavior, no label changes on click.
  **Done when:** The button renders in the header in both light and dark themes. It is keyboard-focusable and activatable with Enter and Space (native `<button>` behavior). Its visible label is the static default `◐ Auto`.

- [ ] 4. Add toggle logic to the main script in `src/clients/plan_status_page.html`.
  **Prerequisites:** Steps 1, 2, 3 (CSS selectors, anti-flash script, button markup all in place).
  In the existing `<script>` block at the bottom of `<body>`, add:
  - A function that reads the current effective theme (check `document.documentElement.dataset.theme`; `"light"` and `"dark"` are forced, anything else — including absent — is `"auto"`) and updates `#theme-toggle` text to `◐ Auto`, `☀ Light`, or `🌙 Dark`.
  - A click handler on `#theme-toggle` that cycles through `auto → light → dark → auto`:
    - `"light"` / `"dark"`: set `document.documentElement.dataset.theme` to the value, call `localStorage.setItem("determined-theme", value)`.
    - `"auto"`: `delete document.documentElement.dataset.theme`, call `localStorage.removeItem("determined-theme")`.
    - Update the button label after every change.
  - Call the label-sync function once on page load so the button reflects the theme restored by Step 2's anti-flash script (or the default auto state).
  **Done when:** Clicking the button cycles through all three states with immediate visual restyle and correct label text. Choosing light or dark, then reloading the page, restores the forced theme with the correct button label. Choosing auto clears `localStorage["determined-theme"]` (verified in DevTools Application → Local Storage) and returns the page to OS-driven styling. The button label on page load matches the active theme (Auto when no stored preference, Light/Dark when forced).

- [ ] 5. Extend `assertPageServed` in `tests/plan_status_server_test.go` to assert the theme toggle contract.
  **Prerequisites:** Steps 1–4 (all HTML changes complete).
  Add these exact substring markers to the existing marker list: `id="theme-toggle"`, `html[data-theme`, `"determined-theme"`. Add a position assertion: the byte index of `"determined-theme"` in the page body is less than the byte index of `<style>`, proving the anti-flash script runs before CSS. The existing markers (`determined — planning`, `EventSource`, `banner`) remain unchanged.
  **Done when:** `go test -count=1 ./tests/ -run TestPlanStatusServerContract` fails if any of the three new markers is removed from the embedded HTML or if the anti-flash script ordering invariant is broken, and passes with the full change in place.

- [ ] 6. Run the full test suite.
  **Prerequisite:** Step 5.
  `go test -count=1 ./...`
  **Done when:** `go test -count=1 ./...` exits zero with all tests passing, including `TestPlanStatusServerContract`.

- [ ] 7. Manual smoke test.
  **Prerequisites:** Steps 1–6 (all code changes and automated tests complete and passing).
  Run `determined -plan "<goal>" -interactive` (any goal text) and open the printed URL in a browser. Verify the five Observable Success Criteria from PLAN.md:
  (a) No stored preference → theme follows OS in both light and dark OS modes.
  (b) Clicking the toggle cycles auto → light → dark → auto with immediate restyle and correct button label.
  (c) Choosing light or dark, then hard-reloading the page, restores the forced theme.
  (d) Choosing auto clears the stored preference and returns to OS-driven styling.
  (e) SSE updates still render goal, plan, steps, and banner while toggling themes.
  **Done when:** All five criteria are confirmed in at least one Chromium-based browser and one Firefox-based browser. **If any criterion fails, return to Step 1, fix the issue, and re-run Steps 1–7.**

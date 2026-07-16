# STEPS: Theme Toggle for Interactive-Mode Status Page

- [ ] 1. Restructure theme CSS in `src/clients/plan_status_page.html`.
  Open the file and locate the existing `<style>` block. Replace the current
  `@media (prefers-color-scheme: dark) { :root { ... } }` block (lines 15–22)
  with the four-block structure below, preserving the existing `:root` light
  variables (lines 8–14) exactly as they are:
  (a) `:root` block — **leave untouched** (lines 8–14).
  (b) Replace `@media (prefers-color-scheme: dark) { :root { ... } }` with
      the media query using `html:not([data-theme="light"])`:
      ```css
      /* KEEP IN SYNC with the dark variable block in the html[data-theme="dark"] selector below */
      @media (prefers-color-scheme: dark) {
        html:not([data-theme="light"]) {
          --bg: #101418; --fg: #e6e8eb; --card: #1a1f26; --muted: #98a2b3;
          --border: #2b323c; --accent: #60a5fa;
          --ok-bg: #0b2a1c; --ok-fg: #75e0a7; --ok-border: #14532d;
          --bad-bg: #2a1211; --bad-fg: #f97066; --bad-border: #7f1d1d;
        }
      }
      ```
  (c) Add the `html[data-theme="dark"]` block immediately after the media
      query's closing `}` (outside it, same indentation level):
      ```css
      /* KEEP IN SYNC with the dark variable block in the media query above */
      html[data-theme="dark"] {
        --bg: #101418; --fg: #e6e8eb; --card: #1a1f26; --muted: #98a2b3;
        --border: #2b323c; --accent: #60a5fa;
        --ok-bg: #0b2a1c; --ok-fg: #75e0a7; --ok-border: #14532d;
        --bad-bg: #2a1211; --bad-fg: #f97066; --bad-border: #7f1d1d;
      }
      ```
  (d) Add `color-scheme` overrides after the `html[data-theme="dark"]` block,
      before the existing `* { box-sizing: border-box; }` rule:
      ```css
      html[data-theme="light"] { color-scheme: light; }
      html[data-theme="dark"] { color-scheme: dark; }
      ```
  **Done when:**
  ```sh
  go build -o ./bin/determined .
  ```
  succeeds (proving the embedded HTML compiles), and
  `grep -n 'data-theme' src/clients/plan_status_page.html` prints lines
  containing each of these selectors:
  - `html:not([data-theme="light"])`
  - `html[data-theme="dark"]`
  - `html[data-theme="light"]`
  and `grep -c 'KEEP IN SYNC' src/clients/plan_status_page.html` prints `2`.
  To verify the two dark variable blocks are identical, run:
  ```sh
  diff <(sed -n '/KEEP IN SYNC with the dark variable block in the html/,/^[[:space:]]*}/p' src/clients/plan_status_page.html | sed -n '/--bg:/,/--bad-border/p') <(sed -n '/KEEP IN SYNC with the dark variable block in the media/,/^[[:space:]]*}/p' src/clients/plan_status_page.html | sed -n '/--bg:/,/--bad-border/p')
  ```
  which must produce no output (no differences). If the `diff` produces
  output, the variable values drifted between blocks — fix before proceeding.

- [ ] 2. Add an anti-flash inline script in `src/clients/plan_status_page.html`.
  **Prerequisite:** Step 1 (CSS selectors `html:not([data-theme="light"])`,
  `html[data-theme="dark"]`, `html[data-theme="light"]` must exist).
  In `<head>`, after the `<meta name="viewport" content="width=device-width, initial-scale=1">`
  line (line 5) and before the `<style>` opening tag (line 7), insert:
  ```html
  <script>
  (function(){try{var t=localStorage.getItem("determined-theme");if(t==="light"||t==="dark")document.documentElement.dataset.theme=t;}catch(e){}})();
  </script>
  ```
  The try/catch ensures the page never throws when `localStorage` is
  unavailable (privacy-restricted contexts, Safari private browsing,
  storage-full). All other values (missing key, `null`, `"auto"`, bogus
  strings, or a caught exception) leave `data-theme` unset. Placing this
  script before `<style>` is the mechanical guarantee against a flash of wrong
  theme: the `data-theme` attribute is set before the browser parses CSS, so
  the correct variable set is selected on first paint.
  **Done when:**
  ```sh
  go build -o ./bin/determined . && ./bin/determined -plan "test" -interactive
  ```
  succeeds. Open the URL printed by the command in a browser. Open
  `view-source:` of that URL and confirm the substring `"determined-theme"`
  appears before `<style>` in the raw HTML source. Then, in the browser
  console, perform these steps **in this exact order**:
  1. `localStorage.setItem("determined-theme", "dark")` → hard-reload
     (Cmd+Shift+R) → DevTools Elements panel shows `<html>` has
     `data-theme="dark"`.
  2. `localStorage.removeItem("determined-theme")` → hard-reload → `<html>`
     has no `data-theme` attribute.

- [ ] 3. Add a theme toggle button to the header in
  `src/clients/plan_status_page.html`.
  **Prerequisite:** Step 1 (CSS selectors are in place so new `#theme-toggle`
  rules land in a settled stylesheet).
  In `<header>`, after the `.git` div (the `<div class="git">...</div>`
  enclosing the remote and branch spans), add:
  `<button id="theme-toggle" type="button" aria-label="Switch color scheme">◐ Auto</button>`
  At the end of the `<style>` block, immediately before `</style>`, add:
  ```css
  #theme-toggle {
    background: var(--card); color: var(--fg);
    border: 1px solid var(--border); border-radius: 0.4rem;
    padding: 0.25rem 0.6rem; font-size: 0.85rem; cursor: pointer;
    line-height: 1.4;
  }
  #theme-toggle:hover {
    background: var(--accent); color: #ffffff; border-color: var(--accent);
  }
  #theme-toggle:focus-visible {
    outline: 2px solid var(--accent); outline-offset: 2px;
  }
  ```
  This step adds **static markup and styling only** — no JS behavior, no
  label changes on click.
  **Done when:**
  ```sh
  go build -o ./bin/determined . && ./bin/determined -plan "test" -interactive
  ```
  succeeds. Open the URL printed by the command. The `◐ Auto` button renders
  in the header to the right of the git info. Hovering changes its background
  to the accent color with white text and accent border. Keyboard-tabbing to
  it shows a visible focus ring (`outline: 2px solid` in the accent color).
  It is activatable with Enter and Space (native `<button>` behavior) though
  clicking has no effect yet. Verify these observations in both light and dark
  OS modes. To toggle the OS color scheme in DevTools: open DevTools, then
  ⋮ menu → More tools → Rendering → scroll to "Emulate CSS media feature
  prefers-color-scheme" and select `prefers-color-scheme: light` or
  `prefers-color-scheme: dark`.

- [ ] 4. Add toggle logic to the main script in
  `src/clients/plan_status_page.html`.
  **Prerequisites:** Steps 1, 2, 3 (CSS selectors, anti-flash script, button
  markup all in place).
  In the existing `<script>` block at the bottom of `<body>`, after the
  `events.onerror = () => el("disconnected").classList.add("on");` line and
  before the closing `</script>` tag, add:
  ```js
  function syncThemeLabel() {
    const t = document.documentElement.dataset.theme;
    const btn = document.getElementById("theme-toggle");
    if (t === "light") btn.textContent = "\u2600 Light";
    else if (t === "dark") btn.textContent = "\uD83C\uDF19 Dark";
    else btn.textContent = "\u25D0 Auto";
  }
  document.getElementById("theme-toggle").addEventListener("click", function() {
    const cur = document.documentElement.dataset.theme;
    let next;
    if (cur === "light") {
      next = "dark";
      document.documentElement.dataset.theme = "dark";
    } else if (cur === "dark") {
      next = null;
      delete document.documentElement.dataset.theme;
    } else {
      next = "light";
      document.documentElement.dataset.theme = "light";
    }
    try {
      if (next) {
        localStorage.setItem("determined-theme", next);
      } else {
        localStorage.removeItem("determined-theme");
      }
    } catch(e) {}
    syncThemeLabel();
  });
  syncThemeLabel();
  ```
  This code lives inside the existing `"use strict"` script block. Variable
  declarations use `const` and `let`, consistent with the existing script
  style (the existing `const el = (id) => document.getElementById(id)` helper
  is the reference for style conventions).
  The ring order is **auto → light → dark → auto** regardless of starting
  point: when the page loads with a stored `"dark"`, the first click
  transitions to `"auto"` (not `"light"`).
  **Done when:**
  ```sh
  go build -o ./bin/determined . && ./bin/determined -plan "test" -interactive
  ```
  succeeds. Open the URL printed by the command and verify:
  (i) Click the button: label cycles `◐ Auto` → `☀ Light` → `🌙 Dark` →
      `◐ Auto` with immediate visual restyle at each click.
  (ii) Set to `☀ Light`, hard-reload: page loads in light theme with
       `☀ Light` label. Click once: transitions to `🌙 Dark` (not `◐ Auto`).
  (iii) Set to `🌙 Dark`, hard-reload: page loads in dark with `🌙 Dark`
        label. Click once: transitions to `◐ Auto`. In DevTools Elements
        panel, confirm `<html>` has no `data-theme` attribute.
  (iv) From auto, DevTools Application → Local Storage: key
       `"determined-theme"` is absent. From light or dark, the key is present
       with the correct value.
  (v) Bogus stored value resilience: in the console, run
      `localStorage.setItem("determined-theme", "bogus")`, hard-reload.
      Confirm `<html>` has no `data-theme` attribute and the button reads
      `◐ Auto`. No console error appears.

- [ ] 5. Extend `assertPageServed` in `tests/plan_status_server_test.go` to
  assert the theme toggle contract.
  **Prerequisites:** Steps 1–4 (all HTML changes complete).
  Edit `tests/plan_status_server_test.go`.

  **(A)** Add `"reflect"` and `"regexp"` to the imports block:
  ```go
  import (
      "bufio"
      "context"
      "io"
      "net/http"
      "reflect"
      "regexp"
      "strings"
      "testing"
      "time"

      "determined/src/clients"
      "determined/src/models"
  )
  ```

  **(B)** Replace the existing `assertPageServed` function with the version
  below:
  ```go
  func assertPageServed(t *testing.T, url string) {
      t.Helper()
      resp, err := http.Get(url)
      if err != nil {
          t.Fatalf("fetch page: %v", err)
      }
      defer resp.Body.Close()
      if resp.StatusCode != http.StatusOK {
          t.Fatalf("page status = %d, want 200", resp.StatusCode)
      }
      body, err := io.ReadAll(resp.Body)
      if err != nil {
          t.Fatalf("read page: %v", err)
      }
      page := string(body)

      // --- substring markers (regression + new theme contract) ---
      for _, marker := range []string{
          "determined — planning", "EventSource", "banner",
          "step-card", "taskSteps", "Done when: ",
          `id="theme-toggle"`, `html:not([data-theme="light"])`,
          `html[data-theme="dark"] {`, `"determined-theme"`,
          "color-scheme: light dark",
          `html[data-theme="light"] { color-scheme: light; }`,
          `html[data-theme="dark"] { color-scheme: dark; }`,
      } {
          if !strings.Contains(page, marker) {
              t.Errorf("page missing %q", marker)
          }
      }

      // --- both KEEP IN SYNC comments must be present ---
      if n := strings.Count(page, "KEEP IN SYNC"); n != 2 {
          t.Errorf("KEEP IN SYNC count = %d, want 2", n)
      }

      // --- anti-flash script must appear before <style> ---
      themeIdx := strings.Index(page, `"determined-theme"`)
      styleIdx := strings.Index(page, "<style>")
      if themeIdx == -1 || styleIdx == -1 || themeIdx >= styleIdx {
          t.Errorf("anti-flash script position: %q at byte %d, <style> at byte %d — want %q before <style>",
              "determined-theme", themeIdx, "<style>", styleIdx, "determined-theme")
      }

      // --- light-theme :root variables must match today's values exactly ---
      for _, want := range []string{
          "--bg: #f6f7f9;", "--fg: #1c1e21;", "--card: #ffffff;",
          "--muted: #667085;", "--border: #e4e7ec;", "--accent: #2563eb;",
          "--ok-bg: #ecfdf3;", "--ok-fg: #067647;", "--ok-border: #abefc6;",
          "--bad-bg: #fef3f2;", "--bad-fg: #b42318;", "--bad-border: #fecdca;",
      } {
          if !strings.Contains(page, want) {
              t.Errorf(":root light variable missing or changed: %s", want)
          }
      }

      // --- dark-block sync: extract and compare variable values ---
      mediaVars := extractDarkVars(page,
          `KEEP IN SYNC with the dark variable block in the html[data-theme="dark"] selector below`)
      explicitVars := extractDarkVars(page,
          `KEEP IN SYNC with the dark variable block in the media query above`)
      if len(mediaVars) == 0 {
          t.Error("no dark variables extracted from media query block")
      }
      if len(explicitVars) == 0 {
          t.Error("no dark variables extracted from html[data-theme=\"dark\"] block")
      }
      if !reflect.DeepEqual(mediaVars, explicitVars) {
          t.Errorf("dark variable blocks differ:\n  media query: %v\n  explicit:    %v",
              mediaVars, explicitVars)
      }
  }
  ```

  **(C)** Add the `extractDarkVars` helper function after `assertPageServed`:
  ```go
  // extractDarkVars extracts --var: value; pairs from the dark variable block
  // identified by syncComment (a KEEP IN SYNC comment unique to that block).
  // It locates the comment, finds --bg: after it (which only exists inside the
  // variable block), scans backward to find the opening {, then brace-matches
  // to the closing } and extracts every --var-name: value; declaration.
  func extractDarkVars(page, syncComment string) map[string]string {
      commentIdx := strings.Index(page, syncComment)
      if commentIdx == -1 {
          return nil
      }
      // Find --bg: after the comment — this uniquely anchors us in the variable block.
      bgIdx := strings.Index(page[commentIdx:], "--bg:")
      if bgIdx == -1 {
          return nil
      }
      // Scan backward from --bg: to find the nearest opening brace.
      open := strings.LastIndex(page[commentIdx:commentIdx+bgIdx], "{")
      if open == -1 {
          return nil
      }
      start := commentIdx + open + 1

      // Brace-match to find the closing brace.
      depth := 1
      end := start
      for i := start; i < len(page) && depth > 0; i++ {
          switch page[i] {
          case '{':
              depth++
          case '}':
              depth--
              if depth == 0 {
                  end = i
              }
          }
      }
      if depth != 0 {
          return nil
      }
      block := page[start:end]
      vars := make(map[string]string)
      re := regexp.MustCompile(`(--[\w-]+:\s*[^;]+;)`)
      for _, m := range re.FindAllString(block, -1) {
          parts := strings.SplitN(m, ":", 2)
          if len(parts) == 2 {
              vars[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
          }
      }
      return vars
  }
  ```

  The `extractDarkVars` function anchors on the unique `KEEP IN SYNC` comment,
  then uses `--bg:` (which only appears in dark variable blocks) to locate the
  block's opening `{` by scanning backward. This avoids coupling the test to
  cosmetic whitespace differences between the variable-block selector and the
  `color-scheme` override selector. The test compares the two maps with
  `reflect.DeepEqual` — a mismatch means the dark variable values drifted
  between blocks or a selector was changed.
  **Done when:**
  ```sh
  go test -count=1 ./tests/ -run TestPlanStatusServerContract
  ```
  passes with all markers present, `KEEP IN SYNC` count equal to 2, position
  invariant satisfied, all twelve light `:root` variables matching, and
  extracted dark variable maps equal.

- [ ] 6. Run the full test suite.
  **Prerequisites:** Steps 1–5 (all code changes and the contract test are
  complete). Step 5 transitively requires Steps 1–4; if Step 5 passed, the
  HTML changes are verified.
  ```sh
  go test -count=1 ./...
  ```
  **Done when:** `go test -count=1 ./...` exits zero with all tests passing,
  including `TestPlanStatusServerContract`.

- [ ] 7. Manual smoke test.
  **Prerequisites:** Steps 1–6 (all code changes and automated tests complete
  and passing).
  Build and run:
  ```sh
  go build -o ./bin/determined . && ./bin/determined -plan "<goal>" -interactive
  ```
  (any goal text). Open the printed URL in a browser.

  **7a. Functional criteria (verify in both a Chromium-based and a
  Firefox-based browser):**
  (a) No stored preference → theme follows OS in both light and dark OS modes.
      To toggle the OS color scheme per browser: **Chrome/Edge** — DevTools →
      ⋮ menu → More tools → Rendering → "Emulate CSS media feature
      prefers-color-scheme". **Firefox** — DevTools → ⋮ menu → Settings →
      "Enable browser chrome and add-on debugging toolboxes", then open
      Browser Toolbox and use the Rendering pane. Alternatively, toggle the
      actual OS setting (System Preferences / Settings → Appearance).
  (b) Clicking the toggle cycles auto → light → dark → auto with immediate
      restyle and correct button label.
  (c) Choosing light or dark, then hard-reloading the page, restores the
      forced theme with the correct label.
  (d) Choosing auto clears the stored preference (verify in DevTools
      Application → Local Storage) and returns to OS-driven styling.
  (e) SSE updates still render goal, plan, steps, and banner while toggling
      themes — verify content panels populate after the plan is produced.
  (f) `color-scheme` verification: in DevTools Console, run
      `getComputedStyle(document.documentElement).colorScheme` in each of the
      three states. Expect `"light dark"` in auto, `"light"` under forced
      light, `"dark"` under forced dark.

  **7b. localStorage unavailability resilience (two code paths):**
  (g1) **Anti-flash script (on page load):** verify the anti-flash `<script>`
       block from Step 2 wraps every `localStorage` call in a try/catch with
       an empty catch block. Confirm by reading the source: every code path
       through the try block that reaches a `localStorage` call, and every
       exception path through the catch block, leaves `data-theme` unset.
       Additionally, load the page in a browser context that blocks
       localStorage at the API level. Options (any one is sufficient):
       - Safari Private Browsing window.
       - Firefox: set `dom.storage.enabled=false` in `about:config`.
       - Chrome/Edge: DevTools → Application → Storage → uncheck "Enable
         Local Storage" (if available in your version), or use an incognito
         window with "Block third-party cookies" set to strict.
       Confirm no console error appears and `<html>` has no `data-theme`
       attribute. If no localStorage-blocking browser context is available,
       the code-review verification alone is sufficient — every localStorage
       access in the anti-flash script is visibly inside the try block.
  (g2) **Toggle click handler (after page load):** verify the click handler
       from Step 4 wraps every `localStorage.setItem`/`removeItem` call in a
       try/catch with an empty catch block. Confirm by reading the source:
       if the try block throws, the catch block is empty, `syncThemeLabel()`
       still runs (it is after the try/catch), and the button cycles visually
       without the value being persisted.

  **7c. Bogus stored value resilience:**
  (h) In the console, run `localStorage.setItem("determined-theme", "bogus")`,
      hard-reload. Confirm `<html>` has no `data-theme` attribute, button
      reads `◐ Auto`, and no console error appears.

  **Done when:** Criteria (a)–(f) confirmed in both browser engines, (g1)
  confirmed via code review plus at least one browser with blocked
  localStorage, (g2) confirmed via code review, and (h) confirmed in one
  browser. **If any criterion fails, diagnose which step (1–4) caused the
  failure, fix it, and re-run from that step forward through Step 7.**

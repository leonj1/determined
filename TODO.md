# TODO: Restyle plan status page to editorial/newspaper theme

Target file: `src/clients/plan_status_page.html` (served by `src/clients/plan_status_server.go`).
Goal: match screenshot aesthetic — "Nexigent" style editorial layout: white paper background,
serif display headings, monospace uppercase metadata, thin black horizontal rules instead of
cards, warm orange accent, no rounded corners or shadows.

## Design tokens (from screenshot)

- [ ] Replace `:root` light palette variables:
  - `--bg: #ffffff` (plain white, no gray page background)
  - `--fg: #111111` (near-black text)
  - `--card: #ffffff` (sections are not boxes; keep variable for form fields)
  - `--muted: #9a9a9a` (light gray for monospace metadata like `2H AGO`, `TO: ...`)
  - `--border: #111111` for section-heading rules; add `--rule-light: #e5e5e5` for subtle inner separators
  - `--accent: #e05d38` (warm orange-red, used for the `→` marker in screenshot)
  - Keep ok/bad colors but mute them (thin-border, no filled pill backgrounds)
- [ ] Fonts:
  - Headings + body prose: serif stack — `Georgia, 'Times New Roman', Times, serif`
    (screenshot uses a Tiempos-like editorial serif; Georgia is the closest system font)
  - Metadata, timestamps, URLs, log output: `ui-monospace, 'SF Mono', Menlo, monospace`,
    uppercase with `letter-spacing: 0.08em` where it mimics the screenshot labels
- [ ] Dark theme: derive equivalent palette (`#111` background, `#eee` text, same accent);
  update both `:root[data-theme="dark"]` and the `prefers-color-scheme` block. Keep the
  existing toggle JS untouched.

## Structural style changes (CSS only — no HTML/JS behavior changes)

- [ ] `body`: white background, serif font, generous horizontal padding (~2rem),
  max-width unconstrained like screenshot (columns spread full width).
- [ ] `header`: drop card background and border; large serif title ("determined — planning"
  styled like "Nexigent" masthead, ~1.6rem serif, weight 400/500), full-width 1px black
  rule underneath. Git remote/branch becomes uppercase monospace muted text.
- [ ] `nav#tabs`: replace accent-underline pill tabs with editorial tabs — serif bold labels,
  active tab gets 2px black underline (not blue), inactive muted. Remove card background.
- [ ] `section`: remove card look entirely — no background, no border, no border-radius,
  no padding box. Each `section h2` becomes the screenshot's column header: bold serif
  (~1.15rem), normal case (screenshot uses "Polsia", "Documents", "Email" in title case,
  not uppercase), followed by full-width 1px black bottom rule with small gap.
- [ ] `#banner`, `#waiting`, `#disconnected`: flat editorial notices — no filled background,
  square corners, thin top/bottom rules or left accent bar; orange `→` prefix via
  `::before` for status lines (mirrors screenshot's arrow before "Welcome to Nexigent!").
- [ ] `.step-card`, `.log-entry`: replace bordered rounded cards with rule-separated list
  items: `border: 0; border-bottom: 1px solid var(--rule-light); border-radius: 0`.
  Step number / badge / timestamps become uppercase monospace muted (like `2H AGO`).
  "Done" badge: plain monospace text (`✓ DONE`), no pill background.
- [ ] Right-aligned timestamps in log/step heads (screenshot pattern: title left, `2H AGO` right).
- [ ] `aside#activity`: same column treatment — "Activity" serif header with black rule,
  entries separated by light rules; timestamps monospace uppercase; spinner accent stays orange.
- [ ] Buttons (`#implement`, quiz buttons, annotate, theme toggle): square corners
  (`border-radius: 0`), either solid black background with white text or thin 1px black
  outline; hover inverts. `VIEW ALL`-style links: uppercase monospace with underline.
- [ ] `.doc` prose: serif body text; code blocks keep monospace with `--rule-light` border,
  square corners, white/very-light background. `blockquote` left bar becomes black or accent.
- [ ] Diff/quiz/token colors: keep semantic green/red but as text color on white with thin
  borders — no filled `--ok-bg`/`--bad-bg` panels in light theme (or soften to near-white tints).
- [ ] Sequence diagram (`.seq-*`): black strokes, square actor boxes (`rx="0"` is SVG-side —
  acceptable to leave; recolor via CSS vars only).

## Verification

- [ ] Run `determined` interactive mode (or open the HTML with a stub status payload) and
  compare against screenshot: masthead, column rules, monospace metadata, orange accent.
- [ ] Toggle theme: auto → light → dark all render coherently, no flash on reload.
- [ ] Check all 8 tabs plus tests sub-tabs, quiz flow, annotate forms, exec log rendering.
- [ ] `go test ./...` — confirm no test asserts on the old CSS.

## Notes

- All changes confined to `<style>` block (plus possible small class additions in static
  HTML). No JS behavior edits needed.
- SVG diagram rect corner radius (`rx="5"`) is generated in JS `renderSequenceDiagram`;
  change to `rx="0"` if square corners wanted there too (one-line JS constant edit).

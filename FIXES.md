# FIXES

## SECURITY: Stored/DOM XSS via unrestricted link & image URL schemes (regression from marked swap)

**Severity:** High (script execution in the operator's status-page browser context).

**Where:** `src/clients/plan_status_page.html`, `markdownToHtml` renderer overrides — `link` (lines 808-813) and the missing `image` override (marked default renderer used).

**What changed / how introduced:** Step 4 replaced the hand-rolled markdown parser with `marked@16.4.2`. The old parser rendered links via the regex `\[([^\]]+)\]\((https?:[^)\s]+)\)` (commit `3b0a4db`, line 773) — it matched **only** `http:`/`https:` URLs; any other scheme (`javascript:`, `data:`, `vbscript:`) was left as inert literal text and never became a clickable `<a>`. The old parser had **no image support at all**. The new `link` override accepts **any** scheme, and marked's default (un-overridden) `image` renderer emits `<img src>` with any scheme. `escapeHtml` on the href only prevents attribute-quote breakout — it does not neutralize the `javascript:` scheme.

**Evidence (rendered through the vendored `marked.min.js` with the current overrides):**

```
"[click](javascript:alert(1))"       => "<p><a href=\"javascript:alert(1)\" target=\"_blank\" rel=\"noopener\">click</a></p>\n"
"[click](JaVaScRiPt:alert(1))"       => "<p><a href=\"JaVaScRiPt:alert(1)\" ...>click</a></p>\n"
"![alt](javascript:alert(1))"        => "<p><img src=\"javascript:alert(1)\" alt=\"alt\"></p>\n"
```

The rendered markdown is written straight to `innerHTML` (line 828 `node.innerHTML = markdownToHtml(...)`, plus 1028, 1471, 1501, 1512, 1527). A clickable `javascript:` anchor executes script on click in the operator's browser.

**Trust boundary:** the rendered text is `status.goal`, `status.plan`, `status.explanation`, chat entry bodies, and step `text`/`purpose`/`doneWhen` — all LLM/plan-derived content, not operator-authored, and reachable by prompt injection into the planning model. Content crosses an untrusted boundary into DOM-as-HTML.

**Note:** raw block/inline HTML escaping is intact (`<script>`, `<img onerror>` as raw HTML are escaped correctly). Attribute-quote breakout in href is also blocked. The gap is **URL scheme**, not tag escaping — the pre-swap behavior (http/https only) was silently dropped.

**Suggested remediation:** In the `link` override (and add an `image` override, or drop images entirely to match pre-swap behavior), allow only `http:`/`https:` (and optionally `mailto:`) schemes after trimming/normalizing; render disallowed-scheme links as escaped plain text (matching the old parser, which never linkified them). Guard against leading control chars/whitespace that browsers strip before scheme parsing. Add exact-output tests asserting `[x](javascript:alert(1))` and `![x](javascript:alert(1))` do NOT produce an executable `href`/`src`.

## RELIABILITY: TOC anchor desync — nested ATX headings advance the shared dedup counter (regression from marked swap)

**Severity:** Medium (wrong TOC scroll target; no security impact).

**Where:** `src/clients/plan_status_page.html`, `markdownToHtml` `heading` renderer override (lines 813-818) vs `planSections` (lines 854-870).

**What changed / how introduced:** The `heading` override gates ids on `token.raw` starting with `#` (ATX), intending lockstep with `planSections`' ATX-only top-level scan. But marked also emits `heading` tokens for ATX headings **nested inside blockquotes and list items** — their `raw` still starts with `#`, so they receive a body id and advance the shared `headingCounts`. `planSections` scans raw lines (`^(#{2,4})\s`) and never matches `> ## X` or `- ## X`, so its counter diverges. The old parser recursed into blockquotes with a **fresh** counts object, so top-level ids stayed aligned with the TOC.

**Evidence (current renderer, `{headingIds:true, headingPrefix:"plan--", headingLevels:[2,3,4]}`):**

```
input:  "## A\n\n> ## A\n\n## A"
body:   <h2 id="plan--a">A</h2><blockquote><h2 id="plan--a-2">A</h2></blockquote><h2 id="plan--a-3">A</h2>
TOC:    planSections => [plan--a, plan--a-2]   (skips the blockquote line)
```

The TOC's second "A" link (`plan--a-2`) scrolls to the heading inside the blockquote, not the second top-level section (now `plan--a-3`). Same divergence for `- ## X` inside list items. Trigger requires a duplicate-slug heading nested in a blockquote/list — narrow, but plan/explanation text is LLM-generated and quotes headings routinely.

**Suggested remediation:** Emit body ids only for top-level heading tokens. One approach without touching `planSections`: lex first (`const tokens = md.lexer(preprocessed)`), flag top-level heading tokens (they are exactly the entries of the top-level token array), then render via `md.parser(tokens)` with the `heading` override checking the flag; nested headings render with no id and do not advance the counter. Add a test asserting the `"## A\n\n> ## A\n\n## A"` body ids are `plan--a` / (no id) / `plan--a-2`, matching `planSections`.

### Verification update (2026-07-21, commit 8d48214)

Re-reviewed as reliability pass. Finding **still unresolved**: commit 8d48214 added this FIXES.md entry and NOTES.md text only — no change to `src/clients/plan_status_page.html`. The `heading` override (lines 813-818) still gates ids solely on `token.raw` starting with `#`, so nested ATX headings keep receiving ids and advancing the shared counter. Fresh repro through `createPageEnvironment` (test harness vm, vendored `marked.min.js`):

```
input "## A\n\n> ## A\n\n## A"  (headingIds, prefix plan--, levels [2,3,4])
body: <h2 id="plan--a">A</h2><blockquote>\n<h2 id="plan--a-2">A</h2></blockquote>\n<h2 id="plan--a-3">A</h2>
TOC : ["plan--a","plan--a-2"]

input "- ## B\n\n## B"
body: <ul>\n<li><h2 id="plan--b">B</h2></li>\n</ul>\n<h2 id="plan--b-2">B</h2>
TOC : ["plan--b"]
```

List-item case is worse than first recorded: the only TOC entry (`plan--b`) scrolls to the heading inside the `<li>`, and the top-level `## B` section is unreachable from the TOC. Current suite (23 tests) passes because no test covers nested headings — the step-5 lockstep test from the reopen note is still missing. STEPS.md step 4 was marked `[x]` despite the open reopen note; unchecked it. No new issues found this pass: XSS scheme fix (`isSafeUrl` + `image` override) verified present and tested, `go build ./...` clean, `node --test` 23/23 green.

### Verification update (2026-07-21, commit 0927e7d)

Finding **still unresolved** — and the step was re-checked without a fix. Commits 8d48214 and 0927e7d touched only FIXES.md/NOTES.md/README.md; `src/clients/plan_status_page.html` is unchanged since 110ef5b (the XSS fix). The `heading` override still gates ids solely on `token.raw` starting with `#` (`isAtx` at the `headingIdAttribute` call), so nested ATX headings receive body ids and advance the shared dedup counter while `planSections` skips them. STEPS.md step 4 (and 5, 6) were found marked `[x]` again despite the open reopen note — the loop is closing the step on the passing 23-test suite, which contains no nested-heading test, so the checkbox flips back green each iteration without the lockstep Done-when ever being exercised.

Fresh repro at HEAD through the vm harness (vendored `marked.min.js`, page's last `<script>`):

```
input "## A\n\n> ## A\n\n## A"  (headingIds, prefix plan--, levels [2,3,4])
body: <h2 id="plan--a">A</h2><blockquote>\n<h2 id="plan--a-2">A</h2></blockquote>\n<h2 id="plan--a-3">A</h2>
TOC : ["plan--a","plan--a-2"]

input "- ## B\n\n## B"
body: <ul>\n<li><h2 id="plan--b">B</h2></li>\n</ul>\n<h2 id="plan--b-2">B</h2>
TOC : ["plan--b"]
```

No other new issues this pass: `isSafeUrl` + `image` override intact, raw-HTML escaping intact, `go build ./...` clean, `node --test` 23/23 green. **Process note for the executor:** the fix must land in `src/clients/plan_status_page.html` (top-level-token gating, e.g. lex via `md.lexer` and flag top-level heading tokens before `md.parser`) *and* the step-5 lockstep test must be added — do not re-check step 4 until `markdownToHtml("## A\n\n> ## A\n\n## A", …)` yields body ids `plan--a`/`plan--a-2` for the two top-level headings with no id inside the blockquote, matching `planSections`.

### Verification update (2026-07-21, commit e201e60)

Finding **still unresolved** — third consecutive doc-only iteration. `git diff 110ef5b..HEAD -- src/ tests/` is empty; commits 8d48214, 0927e7d, and e201e60 changed only FIXES.md/NOTES.md/README.md. The `heading` override (`plan_status_page.html:813-816`) still gates ids solely on `isAtx = /^#/.test((token.raw || "").trimStart())`, so nested ATX headings receive body ids and advance the shared dedup counter while `planSections` skips them. STEPS.md step 4 was again found re-checked `[x]`; unchecked again.

Fresh repro at e201e60 (vm + vendored `marked.min.js`, page's last `<script>`):

```
input "## A\n\n> ## A\n\n## A"  (headingIds, prefix plan--, levels [2,3,4])
body: <h2 id="plan--a">A</h2><blockquote>\n<h2 id="plan--a-2">A</h2></blockquote>\n<h2 id="plan--a-3">A</h2>
TOC : ["plan--a","plan--a-2"]
```

`go build ./...` clean, `node --test` 23/23 green — suite still has no nested-heading test, which is exactly why the checkbox keeps flipping back. **The executor loop is stuck re-committing STEPS.md prose as "step 4" without editing source.** Concrete exit condition, restated: (a) edit `src/clients/plan_status_page.html` — in `markdownToHtml`, lex first (`const tokens = md.lexer(unflattenTables(text))`), mark the top-level heading tokens (e.g. `tokens.forEach(t => { if (t.type === "heading") t._topLevel = true; })`), render with `md.parser(tokens)`, and gate the `heading` override's id emission on `token._topLevel` instead of (or in addition to) `isAtx`; (b) add the step-5 lockstep test asserting body ids `plan--a`/`plan--a-2` on the two top-level headings and no id in the blockquote for the input above. Until both land, this entry stays open.

### Verification update (2026-07-21, commit 50ef4c9) — RESOLVED

Fix landed in 50ef4c9: `markdownToHtml` now lexes via `md.lexer(unflattenTables(text))`, flags top-level heading tokens (`token.plansectionEligible = true`), renders via `md.parser(tokens)`, and the `heading` override gates the id on `plansectionEligible && isAtx`. Step-5 lockstep test ("nested headings take no body id and stay in lockstep with planSections") added and passing; suite 24/24 green, `go build ./...` clean. Repro inputs `"## A\n\n> ## A\n\n## A"` and `"- ## B\n\n## B"` now produce body ids matching `planSections`. Entry closed.

## RELIABILITY: TOC anchor desync #2 — `planSections` fence scan diverges from marked fence lexing (`~~~` and indented fences)

**Severity:** Medium-low (phantom/mis-targeted TOC entries and shifted dedup ids; no security impact). Found 2026-07-21 at commit 50ef4c9 during the reliability re-review that closed the nested-heading desync above.

**Where:** `src/clients/plan_status_page.html` — `planSections` (fence toggle `line.startsWith("```")`) vs `markdownToHtml` (marked lexer, which honors CommonMark fences: ` ``` ` **and** `~~~`, each at 0–3 spaces indent).

**What changed / how introduced:** The old hand-rolled parser recognized only column-0 ` ``` ` fences — exactly what `planSections` recognizes — so both sides skipped (or both scanned) the same lines and the shared-slug counters stayed aligned. Marked correctly lexes `~~~` fences and indented (1–3 space) fences as code blocks, so `##`-style lines inside them produce no heading token, while `planSections`' line scan still matches them. The counters diverge and the TOC gains entries with no (or the wrong) body target.

**Evidence (vm harness, vendored `marked.min.js`, page's last `<script>`, `{headingIds:true, headingPrefix:"plan--", headingLevels:[2,3,4]}`):**

```
input "~~~\n## A\n~~~\n\n## A\n\n## A"
body: <pre><code>…## A…</code></pre><h2 id="plan--a">A</h2><h2 id="plan--a-2">A</h2>
TOC : ["plan--a","plan--a-2","plan--a-3"]
      (TOC "A" #1 targets the wrong section, #3 is a dead anchor)

input "  ```\n## C\n  ```\n\n## C"
body: <pre><code>…## C…</code></pre><h2 id="plan--c">C</h2>
TOC : ["plan--c","plan--c-2"]
      (TOC entry #1 is the fenced line; the real heading's TOC link is "plan--c-2" — dead)
```

Trigger is plausible in LLM-generated plans: `~~~` fences are the standard way to quote markdown examples that themselves contain ` ``` `, and fences indented under list items are common.

**Suggested remediation:** Make `planSections` share marked's block lexing instead of maintaining a parallel line scanner — e.g. `const tokens = new marked.Marked({gfm:true}).lexer(unflattenTables(text || ""))`, then walk the **top-level** tokens for `type === "heading" && depth 2–4` with `token.raw` starting `#` (ATX), building ids with the same `uniqueHeadingSlug` counter logic. That makes lockstep structural (one lexer, one token walk) rather than two scanners that must agree, and also removes the duplicated fence-toggle logic. Note: PLAN.md lists `planSections` under "keep unchanged" — that constraint was scoped to the swap itself; the lockstep contract ("body ids stay in lockstep with planSections") is the governing requirement and now requires touching `planSections`. Alternative minimal fix (broaden the fence toggle to `/^ {0,3}(?:`{3,}|~{3,})/` with matching-fence close tracking) re-implements CommonMark fence rules by hand and will drift again; prefer the shared-lexer approach.

## AUDIT (2026-07-21, commit fb2ad6e): TESTS.md journey Test 1 asset-serving assertion not automated

Steps 1-7 verified satisfied in code: asset vendored, embedded, script tag ordered before diff2html, `markdownToHtml` rewritten with lexer/flag/parser gating plus `isSafeUrl` link/image scheme allowlist, dead helpers gone, node suite 25/25 green, `make test` passes, both lockstep tests (nested-heading and fence-style) pass.

Gap against TESTS.md Test 1 (end-to-end journey): the journey requires "The vendored `/assets/marked.min.js` loads (HTTP 200)". The rendering half of the journey is automated in `tests/plan_status_page_test.js` (mermaid SVG, diff dispatch, tok-spans, GFM table, escaped script, deduped ids), but the serving half is not: `tests/plan_status_server_test.go` asserts only the diff2html assets in `assertDiffAssetsServed` (lines 198-199) and its page-marker list (line 134) checks `src="/assets/diff2html.min.js"` but not `src="/assets/marked.min.js"`. If the embed directive or script tag regressed, no automated test would fail. Step 8 appended to STEPS.md to close this.

# Plan: Replace custom markdown parser with marked

## Task type

Refactor (behavior-preserving swap of implementation) with a small vendoring/integration facet.
Primary template: **refactor** (preserved behavior + incremental checks). Secondary: **integration** (vendored library boundary, asset serving).

## Quality gate

- **Intended outcome**: The hand-rolled ~130-line markdown parser in `plan_status_page.html` is replaced by the `marked` library (v16.x, vendored UMD), while all documented custom rendering behaviors are preserved via marked renderer overrides.
- **Target user / use case**: Developers/operators viewing the plan status page (goal/plan/explain/quiz/steps views) rendered by the Go `plan_status_server`. No end-user-visible change intended beyond three accepted edge-case deltas.
- **In scope**:
  - Vendor `marked.min.js` + license/readme under `src/clients/assets/`.
  - Add `marked.min.js` to the `go:embed` directive in `plan_status_server.go`.
  - Add a `<script>` tag loading the asset in `plan_status_page.html`.
  - Rewrite `markdownToHtml` to delegate to a per-call `marked.Marked` instance with renderer overrides for `heading`, `code`, `link`, `html`.
  - Delete now-dead helpers: `renderInline`, `splitRow`, `isTableRow`, `isSeparatorRow`, `renderTable`.
  - Update `tests/plan_status_page_test.js` to load the marked bundle into the vm context and add exact-output tests.
- **Out of scope**:
  - Adding a bundler / npm build step.
  - Changing any Go logic other than the embed directive.
  - Adding a sanitizer dependency (raw-HTML escaping stays via the `html` renderer override).
  - Altering `headingIdAttribute`, `slugify`, `uniqueHeadingSlug`, `unflattenTables`, `highlightCode`, `renderFence`, `renderDiff`, `renderSequenceDiagram`, `looksLikeMarkdown`, `escapeHtml`, `setDoc`, `planSections`, or any `markdownToHtml` call-site signature.
- **Constraints**:
  - No bundler — asset vendored as minified UMD, embedded via `go:embed`, served at `/assets/` (mirrors existing diff2html convention).
  - `markdownToHtml(text, options)` signature unchanged; called at lines 892, 1092, 1535, 1565, 1576, 1591.
  - Per-call `Marked` instance so heading-dedup count state is per-parse.
  - Heading renderer emits no trailing `\n` so existing exact-string assertions (`"<h2>Widget behavior</h2>"`) still pass.
  - Marked v13+ token-object renderer signatures (destructured token args).
  - Fence dispatch normalizes the language as `(token.lang || "").trim().split(/\s+/)[0].toLowerCase()` before calling `renderFence`, so `diff`/`mermaid`/highlighted-lang branches fire on the first info-string word.
  - `unflattenTables` is applied to the raw string *before* `instance.parse()` (not wired as a marked hook/tokenizer).
  - The `heading` renderer emits a body `id` only for ATX headings (gated on `token.raw`), keeping the body dedup counter in lockstep with the ATX-only `planSections` scan.
  - Asset must exist before the embed directive compiles → step order 1→2→3→4→5→6.
- **Observable success criteria**:
  - `make test` (`go test -cover ./...`, which shells `node --test tests/plan_status_page_test.js`) passes.
  - Running server serves `/assets/marked.min.js` with HTTP 200.
  - Status page renders: mermaid `sequenceDiagram` as hand-built SVG, ` ```diff ` via Diff2Html, code fences with `tok-*` highlight spans, raw HTML escaped (no passthrough), duplicate headings deduped (`plan--x`, `plan--x-2`) with working TOC scroll, GFM tables (incl. flattened one-liners via `unflattenTables`).
- **Material risks**:
  - Marked renderer API drift across versions → mitigated by pinning v16.x and documenting version in `README.marked.md`; overrides use v13+ token-object signatures.
  - Trailing-newline / `<p>`-wrapping differences from marked → captured empirically in exact-output tests (accepted deltas).
  - UMD global attach in vm context → harness runs `vm.runInContext(markedSource, context)` before the page script; `marked` attaches to contextified `globalThis`.
  - Minified vendored blob is opaque → license + version README document provenance.
- **Validation approach**: Automated `node --test` unit tests over `markdownToHtml` (exact-string outputs, dedup contract, fence/diff/table/raw-HTML behaviors) driven through Go `make test`; plus one manual smoke pass of the live status page.

### Accepted behavior deltas (record in commit message)
1. One-line flattened fences inside prose render as inline code spans (still escaped), not dispatched fences.
2. Non-heading blocks carry marked's trailing newlines / `<p>`-in-loose-`<li>` wrapping.
3. Setext headings now parse as `h2` (previously paragraph + hr). The `heading` renderer emits a body `id` for ATX headings only (gated on `token.raw` starting with `#`), so setext headings do **not** advance the shared dedup counter — the body ids stay in lockstep with `planSections` (ATX-only `#{2,4}` scan) and TOC scroll is unaffected.

## Verified assumptions (against current tree)

- `plan_status_server.go:27` embed line is exactly `//go:embed assets/diff2html.min.css assets/diff2html.min.js` (separate `//go:embed plan_status_page.html` at line 24). FileServer serves the whole `embed.FS`.
- diff2html vendoring convention present: `assets/{diff2html.min.js,diff2html.min.css,LICENSE.diff2html.md,README.diff2html.md}`.
- diff2html script tag at `plan_status_page.html:551`; new marked tag goes before it.
- Helper line ranges match GOAL.md (`renderInline` 760, `renderTable` 786, `markdownToHtml` 827, `headingIdAttribute` 819, `unflattenTables` 798, etc.).
- `markdownToHtml` call sites at 892, 1092, 1535, 1565, 1576, 1591; plus one recursive call at 862 inside the *old* body (blockquote) that is deleted — marked handles blockquotes natively.
- `tests/plan_status_page_test.js` uses `createPageEnvironment` (line 56) with `vm.runInContext(pageScript, context)` at line 68; `Diff2Html: { html: () => "" }` stub at line 119.
- `make test` = `go test -cover ./...`; `node` v23.11.0 available.

## Refactor template

- **Preserved behavior**: All custom rendering (mermaid SVG, diff, highlight, heading ids/dedup, blank-target links, raw-HTML escaping, table unflattening) preserved via renderer overrides + unchanged helpers. Exact-string assertions are the behavior contract.
- **Incremental checks**: Vendor asset (compilable) → embed (Go compiles) → script tag (page loads) → rewrite layer (unit tests) → test harness (tests green) → manual smoke. Each step has a concrete `Done when:` in STEPS.md. Regression guarded by keeping existing assertions unchanged and adding exact-output tests for the new path.

## Integration template

- **Boundary**: `marked` UMD library at `globalThis.marked`, loaded as a vendored `/assets/` file. Consumed only inside `markdownToHtml`.
- **Failure handling**: Missing asset → embed directive fails to compile (fail-fast at build). In-browser absent `marked` global throws at first `markdownToHtml` call — visible, matching diff2html's existing hard-dependency pattern. No silent fallback added (per literal-requirements convention).

## Files

- `src/clients/plan_status_page.html` — script tag + markdown-layer rewrite + dead-helper deletion.
- `src/clients/plan_status_server.go` — extend embed directive.
- `tests/plan_status_page_test.js` — load marked into vm, add exact-output tests.
- `src/clients/assets/marked.min.js` — new, vendored UMD.
- `src/clients/assets/LICENSE.marked.md`, `src/clients/assets/README.marked.md` — new provenance docs.

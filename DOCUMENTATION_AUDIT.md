# Documentation Audit: determined
Audited at: `84697eb` on 2025-07-31

## Summary Scorecard
| # | Criterion                          | Verdict | One-line reason |
|---|------------------------------------|---------|-----------------|
| 1 | README 30-3 rule                   | PARTIAL | Purpose clear in 30s, but 3-minute quickstart is unworkable without undocumented AI tool prerequisite |
| 2 | CLAUDE.md / AGENTS.md conformance  | FAIL    | CLAUDE.md missing; AGENTS.md exists but is an abbreviated summary missing 8+ required sections |
| 3 | Build/test/run docs for AI agents  | PARTIAL | Build and test commands documented and verified; `make start/stop/restart` absent; no run-command smoke test |
| 4 | PROJECT.md deployment info         | FAIL    | PROJECT.md does not exist at repo root |

**Overall: FAIL** — two criteria FAIL, two PARTIAL.

---

## Detailed Findings

### Criterion 1 — README and the 30-3 Rule

**Verdict: PARTIAL**

#### 30-second read

Title: `# determined`, followed by bold tagline: *"Run an AI coding CLI in a loop until the plan is actually done — verified, not taken on its word."*

First ~150 words explain that `determined` takes a `PLAN.md` + `STEPS.md`, iterates an AI tool of choice (`droid`, `claude`, or `pi`) on each unchecked step, independently verifies each, git-commits verified steps, and only ends after a whole-plan audit. This is concrete: it names the domain (orchestrating AI coding tools for plan execution), who it's for (developers using AI coding CLIs), and what problem it solves (untrusted, unverified AI loop execution).

**PASS — 30-second half.**

#### 3-minute quickstart

The "Getting started" section (README lines 26–65) gives these steps:

1. **Install**: `go build -o determined ./cmd/determined`
2. **Plan** (skip if you have PLAN.md/STEPS.md): `./determined --plan "build a todo CLI"`
3. **Execute**: `./determined -exec`
4. **Watch**: logs land in `logs/`, each verified step becomes a git commit.

**What breaks:**

- **Step 1** works mechanically — `cmd/determined/` exists, `go build` is a standard Go command. But Go is not listed as a prerequisite anywhere in the quickstart. A reader who doesn't have Go installed will fail at the first command with no guidance.

- **Step 2** requires an external AI coding CLI (`droid`, `claude`, or `pi`). This is never stated as a prerequisite in the quickstart. Running `./determined --plan "build a todo CLI"` invokes the default tool (`droid exec`) — if `droid` is not installed, the command fails silently or with an opaque error. The only way to get from `go build` to "it works" without already owning one of these tools is `./determined init`, which is listed as optional ("Optionally install…") and only downloads CLAUDE.md/AGENTS.md files — it does not produce a visible working result.

- **Step 3** requires `PLAN.md` + `STEPS.md` in the working directory, which Step 2 would produce. If Step 2 fails, Step 3 cannot succeed.

- `./determined update` is listed as a "later" command in step 1, but a freshly built binary via `go build` won't be on PATH, so `./determined update` won't be available — this is cosmetic, not a blocker.

- **PLANNING.md** is referenced twice in the README (lines ~96 and ~168: "See [PLANNING.md](PLANNING.md) for details") but the file does not exist in the repository. → Already tracked as issue #82.

- The `make build` path (BUILD.md) requires Docker but Docker is not listed as a prerequisite.

**Verdict on 3-minute half: FAIL.** A new user cannot produce a visible working result in 3 minutes without discovering and installing an external AI tool that is never mentioned as a prerequisite.

Overall: PARTIAL (purpose passes, quickstart fails).

---

### Criterion 2 — CLAUDE.md and AGENTS.md Conformance

**Verdict: FAIL**

#### CLAUDE.md
**Does not exist.** → Already tracked as issue #76.

#### AGENTS.md
**Exists** at repo root (`/AGENTS.md`, 41 lines). → Already tracked as issue #78.

Comparison against the reference at `https://github.com/leonj1/open-doc-format/blob/master/personal-knowledge/AGENTS.md`:

| Reference section | In repo AGENTS.md? |
|---|---|
| Pointer to full OKF bundle + clone/fetch | ✅ Lines 1–9 |
| I/O Interface Pattern | ✅ Listed as "I/O interfaces + Fake impls" |
| Dependency Injection | ✅ Listed as "constructor DI" |
| Literal Requirements / no silent fallbacks | ❌ Missing |
| Size/complexity limits | ✅ Listed as "<700-line classes, <30-line functions, ≤2 indentations" |
| Route discipline | ❌ Missing (though not applicable to this CLI tool) |
| Type discipline | ✅ Listed as "typed arguments, typed objects over primitives, return values not mutations" |
| No static classes/properties | ✅ Listed as "no static" |
| Result types over exceptions | ✅ Listed as "Result types not exceptions" |
| Quality-test requirements | ❌ Missing (no mention of exact results, Fake state assertions, boundary payloads, cardinality) |
| Project layout | ✅ Listed as "src/services, src/clients, src/models, src/routes, tests/" |
| Naming conventions | ✅ Listed as "nouns for classes, verbs for functions" |
| Commit prefixes (FEAT/BUG/CHORE) | ✅ Listed |
| Configuration rules | ❌ Missing |
| Deployment targets (Vercel/Railway) | ✅ Listed |
| Docker + Makefile dev loop | ✅ Listed as "Dockerfiles, docker-compose, Makefile" |
| Elegant Objects principles | ❌ Missing |
| Full Bundle reference | ❌ Missing |
| PROJECT.md directive | ❌ Missing |
| Docs directive | ❌ Missing |

**8 required sections missing:** Literal Requirements, Route Discipline, Quality Tests, Configuration, Elegant Objects, Full Bundle, PROJECT.md directive, Docs directive.

AGENTS.md is a keyword summary (e.g., "Key: I/O interfaces + Fake impls, constructor DI, typed arguments…") — 23 words total for the Code Structure section — compared to the reference's ~3,000 words of detailed rules. An AI agent reading the repo's AGENTS.md gets concept names but no actionable detail.

#### Consistency check
CLAUDE.md is missing, so no divergence to flag.

#### Spot-check: documented vs. practiced

1. **"No static"** — ✅ Practiced. The codebase uses interfaces and constructor injection; no static utility classes found.

2. **"I/O interfaces + Fake impls"** — ✅ Practiced. 30+ interfaces in `src/` (`CommandRunner`, `Clock`, `LogSink`, `FileStore`, `Prompter`, etc.); hand-written Fakes exist in `tests/`; zero mocking framework imports found.

3. **"src/services, src/clients, src/models, src/routes, tests/"** — ⚠️ Partially practiced. `src/services/`, `src/clients/`, `src/models/` exist. `src/routes/` does not exist (this is a CLI tool, understandable but a layout divergence). More critically, **20 test files are co-located under `src/`** (e.g., `src/services/orchestrator_test.go`, `src/clients/clients_test.go`, `src/models/outcome_test.go`) — the convention says tests belong only in `tests/`. → Already tracked as issue #81.

4. **"<700-line classes"** — ❌ Not practiced. `src/services/orchestrator.go` is 1,201 lines. `src/services/plan_orchestrator.go` is 827 lines. Both exceed the 700-line limit. → Already tracked as #83.

5. **Makefile targets (build, test, start, stop, restart)** — ⚠️ Partially practiced. `make build`, `make test`, `make clean` exist. `make start`, `make stop`, `make restart` are absent. → Already tracked as #80.

---

### Criterion 3 — Sufficient Minimal Documentation for an AI Coding Assistant

**Verdict: PARTIAL**

#### Build
**Command:** `go build -o determined ./cmd/determined` (BUILD.md line 1)  
**Alternative:** `make build` (BUILD.md line 20, Makefile line 22)  
**Toolchain:** Go (version `1.25.0` per go.mod; README says 1.24 — mismatch tracked as #77). `make build` additionally requires Docker.  
**Verified:** `cmd/determined/` exists, `go.mod` exists, `Makefile` exists with `build` target, `Dockerfile.build` exists.

✅ **Build is documented and verifiable.** The Go version mismatch (#77) is a minor documentation defect but doesn't prevent discovery.

#### Test
**Command:** `go test -cover ./...` (README line 336, Makefile line 26)  
**Prerequisites:** Go 1.24 (per README; actual requirement is 1.25 per go.mod), Node.js 18+ for browser-behavior tests of the status page.  
**Verified:** `go test` target exists in Makefile. Test files exist in both `tests/` and co-located under `src/`.

⚠️ **Node.js prerequisite** is documented only in the Layout section (README line 332), not in BUILD.md or the quickstart. An AI agent running `go test` without Node.js installed would get failures from the JS tests in `tests/plan_status_page_test.js` and `src/clients/assets/`.

✅ **Test is documented and verifiable**, with the Node.js prerequisite caveat.

#### Run
**Command:** `./determined -exec` (BUILD.md line 2)  
**Prerequisites not documented in quickstart:** Requires `PLAN.md` and `STEPS.md` in the working directory. Requires an AI tool (`droid`/`claude`/`pi`) installed and on PATH.  
**No standalone smoke test exists.** There is no `./determined --version`-only or `./determined init`-only "it works" path that doesn't depend on an external AI tool. `./determined --version` works and is documented in BUILD.md, but it isn't listed as the "verify it works" step in the quickstart.

⚠️ **Run requires undocumented external dependencies.**

#### Makefile completeness
Per the convention standard, a Makefile should have: `build`, `test`, `start`, `stop`, `restart`.  
Present: `build`, `test`, `clean`.  
Missing: `start`, `stop`, `restart`. → Already tracked as #80.

---

### Criterion 4 — PROJECT.md with Deployment Information

**Verdict: FAIL**

**PROJECT.md does not exist** at repo root. → Already tracked as issue #79.

No deployment platform, no production URL, no staging URL documented in any file. The GitHub Releases page delivers binaries (documented in BUILD.md), but that is the *distribution* mechanism, not a *deployment* of a running service. Since `determined` is a CLI tool (not a deployed service), a PROJECT.md would reasonably state "CLI tool, distributed via GitHub Releases — no server deployment" and link to the release page. Even in that form, it is absent.

---

## Remediation Plan

### 1. Create PROJECT.md (highest impact, mechanical fix)
**File:** `/PROJECT.md` (new)

```markdown
# determined

## Deployment
- **Type:** CLI tool — no server deployment.
- **Distribution:** GitHub Releases at https://github.com/leonj1/determined/releases
- **Platforms:** Linux (amd64, arm64), macOS (arm64)
```

### 2. Create CLAUDE.md (mechanical, can copy from AGENTS.md + expand)
**File:** `/CLAUDE.md` (new)

Clone the reference from `https://github.com/leonj1/open-doc-format/blob/master/personal-knowledge/CLAUDE.md` and customize for this Go project. The reference CLAUDE.md and AGENTS.md are identical in content — create one from the other.

### 3. Expand AGENTS.md to full reference
**File:** `/AGENTS.md` (edit)

Add the 8 missing sections from the reference: Literal Requirements, Route Discipline, Quality Tests, Configuration, Elegant Objects, Full Bundle, PROJECT.md directive, Docs directive. The current 41-line summary is useful as a quick-reference header but needs the full rule text below it.

### 4. Create PLANNING.md
**File:** `/PLANNING.md` (new)

Referenced from README lines ~96 and ~168. Either create the file or remove the broken references.

### 5. Fix quickstart prerequisites (README.md)
- List Go 1.25+ as a prerequisite before `go build`.
- List the AI tool requirement explicitly: "You need `droid`, `claude`, or `pi` installed and on PATH."
- Add `./determined --version` as a smoke test between install and plan: "Verify it works: `./determined --version`".

### 6. Add Makefile targets
**File:** `/Makefile` (edit)

Add `start`, `stop`, `restart` targets (even if they just echo "CLI tool — no daemon to manage" for a clean convention pass).

### 7. Move co-located test files to tests/
**File:** 20 `*_test.go` files under `src/` (move to `tests/`)

Convention states tests belong only in `tests/`, never co-located.

### 8. Split oversized classes
**Files:** `src/services/orchestrator.go` (1201 lines), `src/services/plan_orchestrator.go` (827 lines)

Extract sub-orchestrators or collaborator services to bring each class under 700 lines.
